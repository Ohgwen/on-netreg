// Package sync runs the periodic reconciliation loop: for every configured
// UniFi controller, fetch its clients, diff against the registry, and
// apply the resulting DNS changes to Technitium.
package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	stdsync "sync"
	"time"

	"gorm.io/gorm"

	"github.com/Ohgwen/on-netreg/internal/config"
	"github.com/Ohgwen/on-netreg/internal/db"
	"github.com/Ohgwen/on-netreg/internal/netcheck"
	"github.com/Ohgwen/on-netreg/internal/registry"
	"github.com/Ohgwen/on-netreg/internal/settings"
	"github.com/Ohgwen/on-netreg/internal/technitium"
	"github.com/Ohgwen/on-netreg/internal/unifi"
)

// livenessTimeout bounds each per-member ping/TCP liveness probe run during
// identity sync.
const livenessTimeout = 2 * time.Second

// dnsVerifyTimeout bounds the post-write DNS lookup that confirms an
// identity's record actually resolves as expected.
const dnsVerifyTimeout = 3 * time.Second

// UnifiClient is the subset of the UniFi API client the engine depends on.
// Defined here so tests can supply a fake.
type UnifiClient interface {
	FetchClients(ctx context.Context) ([]unifi.NetworkClient, error)
}

// NetworkImporter is implemented by UniFi clients that can list the
// networks configured on their controller. Each cycle the engine imports
// them first, so a device's subnet can be matched against an up-to-date
// network list.
type NetworkImporter interface {
	FetchNetworks(ctx context.Context) ([]unifi.Network, error)
}

// DNSClient is the subset of the Technitium client the engine depends on.
type DNSClient interface {
	AddRecord(ctx context.Context, r technitium.AddRecordRequest) error
	UpdateRecord(ctx context.Context, r technitium.UpdateRecordRequest) error
	DeleteRecord(ctx context.Context, r technitium.DeleteRecordRequest) error
	SetPTR(ctx context.Context, ip, name string, ttl int) error
}

type Engine struct {
	DB        *gorm.DB
	Logger    *slog.Logger
	SecretKey []byte

	// NewUnifiClient/NewDNSClient build clients from settings loaded fresh
	// from the DB each cycle (so edits made in the webapp's Settings pages
	// take effect on the next tick, no restart required). Defaults to
	// unifi.New / technitium.New; overridable in tests.
	NewUnifiClient func(config.UnifiConfig) (UnifiClient, error)
	NewDNSClient   func(config.TechnitiumConfig) DNSClient

	// NewIsAlive builds the liveness probe used to pick which Identity
	// member currently backs a shared DNS record. Defaults to
	// netcheck.New(livenessTimeout); overridable in tests.
	NewIsAlive func() func(ip string) bool
	// VerifyDNS performs the post-write "does this actually resolve"
	// lookup for an Identity's record. Defaults to technitium.Verify;
	// overridable in tests.
	VerifyDNS func(ctx context.Context, dnsHost, fqdn, expectedIP string, timeout time.Duration) error
	// LookupPTR reverse-resolves an address for RepairPTRs. Defaults to
	// technitium.LookupPTR; overridable in tests.
	LookupPTR func(ctx context.Context, dnsHost, ip string, timeout time.Duration) ([]string, error)

	// runMu serializes sync cycles, so a manual "Sync now" never overlaps
	// the background loop's cycle.
	runMu stdsync.Mutex
	// kick carries Kick requests to Run; buffered by one so requests made
	// while a cycle is running coalesce into a single follow-up cycle.
	kickOnce stdsync.Once
	kick     chan struct{}
}

func (e *Engine) kickChan() chan struct{} {
	e.kickOnce.Do(func() { e.kick = make(chan struct{}, 1) })
	return e.kick
}

// Kick asks Run to start a sync cycle now instead of at the next tick, and
// returns without waiting for it.
func (e *Engine) Kick() {
	select {
	case e.kickChan() <- struct{}{}:
	default: // a cycle is already queued
	}
}

func (e *Engine) isAliveFunc() func(ip string) bool {
	if e.NewIsAlive != nil {
		return e.NewIsAlive()
	}
	return netcheck.New(livenessTimeout)
}

func (e *Engine) verifyDNS(ctx context.Context, dnsHost, fqdn, expectedIP string, timeout time.Duration) error {
	if e.VerifyDNS != nil {
		return e.VerifyDNS(ctx, dnsHost, fqdn, expectedIP, timeout)
	}
	return technitium.Verify(ctx, dnsHost, fqdn, expectedIP, timeout)
}

// Run blocks, running one reconciliation cycle immediately and then on
// every tick of the configured poll interval, until ctx is canceled. The
// interval is read once at startup; changing it in Settings takes effect
// on the next process restart.
func (e *Engine) Run(ctx context.Context) {
	e.runOnceLogged(ctx)

	interval := config.Defaults().Unifi.PollInterval
	if appSettings, err := settings.LoadApp(e.DB); err == nil && appSettings.PollInterval > 0 {
		interval = appSettings.PollInterval
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.runOnceLogged(ctx)
		case <-e.kickChan():
			e.runOnceLogged(ctx)
		}
	}
}

func (e *Engine) runOnceLogged(ctx context.Context) {
	if err := e.RunOnce(ctx); err != nil {
		e.Logger.Error("sync cycle failed", "error", err)
	}
}

// RunOnce performs one fetch-reconcile-apply cycle for every enabled UniFi
// controller. One controller failing (e.g. unreachable) does not prevent
// the others from syncing; their errors are joined and returned together.
func (e *Engine) RunOnce(ctx context.Context) error {
	e.runMu.Lock()
	defer e.runMu.Unlock()

	appSettings, err := settings.LoadApp(e.DB)
	if err != nil {
		return fmt.Errorf("loading app settings: %w", err)
	}

	dnsCfg, err := settings.LoadTechnitium(e.DB, e.SecretKey)
	if err != nil {
		return fmt.Errorf("loading technitium settings: %w", err)
	}
	dns := e.NewDNSClient(dnsCfg)

	controllers, err := settings.LoadControllers(e.DB, e.SecretKey)
	if err != nil {
		return fmt.Errorf("loading unifi controllers: %w", err)
	}
	if len(controllers) == 0 {
		e.Logger.Warn("no enabled unifi controllers configured; nothing to sync")
		return nil
	}

	var errs []error
	type controllerClient struct {
		ctrl   settings.ControllerRuntime
		client UnifiClient
	}
	clients := make([]controllerClient, 0, len(controllers))
	for _, ctrl := range controllers {
		client, err := e.NewUnifiClient(ctrl.Config)
		if err != nil {
			e.Logger.Error("sync failed for controller", "controller", ctrl.Name, "error", err)
			errs = append(errs, fmt.Errorf("controller %q: creating unifi client: %w", ctrl.Name, err))
			continue
		}
		e.importNetworks(ctx, ctrl, client)
		clients = append(clients, controllerClient{ctrl: ctrl, client: client})
	}

	// Every known network, from any controller or added by hand, is a
	// candidate: a device is placed by its address alone.
	networks, err := settings.LoadNetworks(e.DB)
	if err != nil {
		return fmt.Errorf("loading networks: %w", err)
	}
	matcher := registry.NewNetworkMatcher(networks)

	var members []db.IdentityMember
	if err := e.DB.Find(&members).Error; err != nil {
		return fmt.Errorf("loading identity members: %w", err)
	}
	skipDNS := make(map[string]bool, len(members))
	for _, m := range members {
		skipDNS[m.MAC] = true
	}

	for _, c := range clients {
		if err := e.syncController(ctx, c.ctrl, c.client, matcher, dns, dnsCfg, appSettings, skipDNS); err != nil {
			e.Logger.Error("sync failed for controller", "controller", c.ctrl.Name, "error", err)
			errs = append(errs, fmt.Errorf("controller %q: %w", c.ctrl.Name, err))
		}
	}

	if err := e.syncIdentities(ctx, dns, dnsCfg, appSettings); err != nil {
		e.Logger.Error("syncing identities failed", "error", err)
		errs = append(errs, fmt.Errorf("syncing identities: %w", err))
	}

	if err := e.syncAliases(ctx, dns, dnsCfg); err != nil {
		e.Logger.Error("syncing aliases failed", "error", err)
		errs = append(errs, fmt.Errorf("syncing aliases: %w", err))
	}

	return errors.Join(errs...)
}

// importNetworks refreshes the networks imported from ctrl, if its client
// can list them. A failure is logged and otherwise ignored: the networks
// imported on an earlier cycle are still good enough to place devices.
func (e *Engine) importNetworks(ctx context.Context, ctrl settings.ControllerRuntime, client UnifiClient) {
	importer, ok := client.(NetworkImporter)
	if !ok {
		return
	}
	networks, err := importer.FetchNetworks(ctx)
	if err == nil {
		err = settings.UpsertNetworks(e.DB, ctrl.ID, networks)
	}
	if err != nil {
		e.Logger.Warn("importing networks failed", "controller", ctrl.Name, "error", err)
	}
}

func (e *Engine) syncController(ctx context.Context, ctrl settings.ControllerRuntime, unifiClient UnifiClient, matcher *registry.NetworkMatcher, dns DNSClient, dnsCfg config.TechnitiumConfig, appSettings db.AppSettings, skipDNS map[string]bool) error {
	seen, err := unifiClient.FetchClients(ctx)
	if err != nil {
		return fmt.Errorf("fetching clients: %w", err)
	}

	seenMACs := make([]string, len(seen))
	for i, c := range seen {
		seenMACs[i] = c.MAC
	}

	// devices.mac is globally unique, not unique per controller. A MAC seen
	// here may already be registered under a different controller (it
	// roamed, the client was re-added to another site, etc.) -- load those
	// rows too, in addition to this controller's own, so Reconcile treats
	// them as an existing device to reassign rather than a new one to
	// insert, which would collide with the global unique index.
	var existing []db.Device
	q := e.DB.Where("controller_id = ?", ctrl.ID)
	if len(seenMACs) > 0 {
		q = q.Or("mac IN ?", seenMACs)
	}
	if err := q.Find(&existing).Error; err != nil {
		return fmt.Errorf("loading existing devices: %w", err)
	}
	seen, existing = claimableClients(ctrl.ID, seen, existing)

	defaultZone := ctrl.DefaultZone
	if defaultZone == "" {
		defaultZone = appSettings.DefaultZone
	}

	// The client's subnet, not the network UniFi says it's on, decides its
	// network: UniFi's network_id can be stale or simply wrong.
	resolver := func(c unifi.NetworkClient) registry.NetworkInfo {
		n, ok := matcher.Match(c.IP, ctrl.ID)
		if !ok {
			return registry.NetworkInfo{Zone: defaultZone}
		}
		zone := n.Zone
		if zone == "" {
			zone = defaultZone
		}
		id := n.ID
		return registry.NetworkInfo{NetworkID: &id, Zone: zone, LeaseSeconds: n.DHCPLeaseTimeSeconds}
	}

	dnsConfig := config.DNSConfig{
		FallbackPattern:        appSettings.FallbackPattern,
		RemoveAfterAbsenceDays: appSettings.RemoveAfterAbsenceDays,
	}

	now := time.Now()
	result := registry.Reconcile(now, ctrl.ID, existing, seen, dnsConfig, resolver, skipDNS)

	outcomes := e.applyChanges(ctx, dns, dnsCfg, result.Changes)

	for i := range result.Devices {
		dev := &result.Devices[i]
		if outcome, ok := outcomes[dev.MAC]; ok {
			dev.DNSRecordSynced = outcome.synced
			dev.LastSyncError = outcome.errMsg
		}
	}

	return e.persist(result.Devices)
}

// claimableClients drops the clients (and their device rows) that belong
// to another controller and that this controller only knows historically.
// A controller's known-client list (/stat/alluser) keeps every MAC it has
// ever seen, usually with no IP, so a device that once visited this site
// but now lives on another would otherwise be reconciled here as
// "no address": its record deleted, then re-created by its own
// controller's pass, every cycle. A device moves to this controller only
// once it's actually connected here.
func claimableClients(controllerID uint, seen []unifi.NetworkClient, existing []db.Device) ([]unifi.NetworkClient, []db.Device) {
	foreign := make(map[string]bool)
	for _, d := range existing {
		if d.ControllerID != 0 && d.ControllerID != controllerID {
			foreign[d.MAC] = true
		}
	}
	if len(foreign) == 0 {
		return seen, existing
	}

	dropped := make(map[string]bool)
	keptSeen := make([]unifi.NetworkClient, 0, len(seen))
	for _, c := range seen {
		if foreign[c.MAC] && !c.Online {
			dropped[c.MAC] = true
			continue
		}
		keptSeen = append(keptSeen, c)
	}
	keptExisting := make([]db.Device, 0, len(existing))
	for _, d := range existing {
		if !dropped[d.MAC] {
			keptExisting = append(keptExisting, d)
		}
	}
	return keptSeen, keptExisting
}

type applyOutcome struct {
	synced bool
	errMsg string
}

func (e *Engine) applyChanges(ctx context.Context, dns DNSClient, dnsCfg config.TechnitiumConfig, changes []registry.Change) map[string]applyOutcome {
	outcomes := make(map[string]applyOutcome, len(changes))

	for _, change := range changes {
		var applyErr error

		switch change.Kind {
		case registry.ChangeCreate:
			applyErr = dns.AddRecord(ctx, technitium.AddRecordRequest{
				Domain:        fqdn(change.Hostname, change.Zone),
				Zone:          change.Zone,
				Type:          "A",
				TTL:           dnsCfg.TTL,
				IPAddress:     change.IPAddress,
				Overwrite:     true,
				PTR:           dnsCfg.CreatePTR,
				CreatePTRZone: dnsCfg.CreatePTR,
			})
		case registry.ChangeUpdate:
			applyErr = dns.UpdateRecord(ctx, technitium.UpdateRecordRequest{
				Domain:        fqdn(change.PreviousHostname, change.PreviousZone),
				Zone:          change.PreviousZone,
				Type:          "A",
				IPAddress:     change.PreviousIPAddress,
				NewDomain:     fqdn(change.Hostname, change.Zone),
				NewIPAddress:  change.IPAddress,
				NewTTL:        dnsCfg.TTL,
				PTR:           dnsCfg.CreatePTR,
				CreatePTRZone: dnsCfg.CreatePTR,
			})
		case registry.ChangeDelete:
			applyErr = dns.DeleteRecord(ctx, technitium.DeleteRecordRequest{
				Domain:    fqdn(change.PreviousHostname, change.PreviousZone),
				Zone:      change.PreviousZone,
				Type:      "A",
				IPAddress: change.PreviousIPAddress,
			})
		}

		// synced tracks whether a DNS record now exists for this device: true
		// after a successful create/update, false after a successful delete
		// (or a failed create/update). A failed delete is the one exception --
		// the old record likely still exists, so it's left marked present.
		outcome := applyOutcome{synced: applyErr == nil && change.Kind != registry.ChangeDelete}
		if applyErr != nil {
			outcome.errMsg = applyErr.Error()
			if change.Kind == registry.ChangeDelete {
				// Record deletion failed; treat it as still present.
				outcome.synced = true
			}
			e.Logger.Error("failed to apply DNS change", "mac", change.MAC, "kind", change.Kind, "error", applyErr)
		} else {
			e.Logger.Info("applied DNS change", "mac", change.MAC, "kind", change.Kind, "hostname", change.Hostname, "zone", change.Zone)
		}
		outcomes[change.MAC] = outcome

		event := db.SyncEvent{
			MAC:       change.MAC,
			Action:    db.SyncEventAction(change.Kind),
			Success:   applyErr == nil,
			Actor:     db.SystemActor,
			CreatedAt: time.Now(),
		}
		if applyErr != nil {
			event.Detail = applyErr.Error()
		}
		if err := e.DB.Create(&event).Error; err != nil {
			e.Logger.Error("failed to record sync event", "error", err)
		}
	}

	return outcomes
}

func (e *Engine) persist(devices []db.Device) error {
	return e.DB.Transaction(func(tx *gorm.DB) error {
		for i := range devices {
			dev := devices[i]
			if dev.ID == 0 {
				if err := tx.Create(&dev).Error; err != nil {
					return err
				}
				continue
			}
			if err := tx.Save(&dev).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func fqdn(hostname, zone string) string {
	if hostname == "" {
		return zone
	}
	return hostname + "." + zone
}
