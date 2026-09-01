// Package sync runs the periodic reconciliation loop: for every configured
// UniFi controller, fetch its clients, diff against the registry, and
// apply the resulting DNS changes to Technitium.
package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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

// DNSClient is the subset of the Technitium client the engine depends on.
type DNSClient interface {
	AddRecord(ctx context.Context, r technitium.AddRecordRequest) error
	UpdateRecord(ctx context.Context, r technitium.UpdateRecordRequest) error
	DeleteRecord(ctx context.Context, r technitium.DeleteRecordRequest) error
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

	networks, err := settings.LoadNetworks(e.DB)
	if err != nil {
		return fmt.Errorf("loading unifi networks: %w", err)
	}
	networksByController := make(map[uint][]db.UnifiNetwork, len(controllers))
	for _, n := range networks {
		networksByController[n.ControllerID] = append(networksByController[n.ControllerID], n)
	}

	var members []db.IdentityMember
	if err := e.DB.Find(&members).Error; err != nil {
		return fmt.Errorf("loading identity members: %w", err)
	}
	skipDNS := make(map[string]bool, len(members))
	for _, m := range members {
		skipDNS[m.MAC] = true
	}

	var errs []error
	for _, ctrl := range controllers {
		if err := e.syncController(ctx, ctrl, networksByController[ctrl.ID], dns, dnsCfg, appSettings, skipDNS); err != nil {
			e.Logger.Error("sync failed for controller", "controller", ctrl.Name, "error", err)
			errs = append(errs, fmt.Errorf("controller %q: %w", ctrl.Name, err))
		}
	}

	if err := e.syncIdentities(ctx, dns, dnsCfg, appSettings); err != nil {
		e.Logger.Error("syncing identities failed", "error", err)
		errs = append(errs, fmt.Errorf("syncing identities: %w", err))
	}

	return errors.Join(errs...)
}

func (e *Engine) syncController(ctx context.Context, ctrl settings.ControllerRuntime, networks []db.UnifiNetwork, dns DNSClient, dnsCfg config.TechnitiumConfig, appSettings db.AppSettings, skipDNS map[string]bool) error {
	unifiClient, err := e.NewUnifiClient(ctrl.Config)
	if err != nil {
		return fmt.Errorf("creating unifi client: %w", err)
	}

	var existing []db.Device
	if err := e.DB.Where("controller_id = ?", ctrl.ID).Find(&existing).Error; err != nil {
		return fmt.Errorf("loading existing devices: %w", err)
	}

	seen, err := unifiClient.FetchClients(ctx)
	if err != nil {
		return fmt.Errorf("fetching clients: %w", err)
	}

	defaultZone := ctrl.DefaultZone
	if defaultZone == "" {
		defaultZone = appSettings.DefaultZone
	}

	networkByUnifiID := make(map[string]db.UnifiNetwork, len(networks))
	for _, n := range networks {
		networkByUnifiID[n.UnifiNetworkID] = n
	}
	resolver := func(c unifi.NetworkClient) registry.NetworkInfo {
		n, ok := networkByUnifiID[c.NetworkID]
		if !ok || c.NetworkID == "" {
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
				Domain:       fqdn(change.PreviousHostname, change.PreviousZone),
				Zone:         change.PreviousZone,
				Type:         "A",
				IPAddress:    change.PreviousIPAddress,
				NewDomain:    fqdn(change.Hostname, change.Zone),
				NewIPAddress: change.IPAddress,
				NewTTL:       dnsCfg.TTL,
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
