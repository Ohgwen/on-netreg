// Package sync runs the periodic reconciliation loop: fetch UniFi clients,
// diff against the registry, and apply the resulting DNS changes to
// Technitium.
package sync

import (
	"context"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/Ohgwen/on-netreg/internal/config"
	"github.com/Ohgwen/on-netreg/internal/db"
	"github.com/Ohgwen/on-netreg/internal/registry"
	"github.com/Ohgwen/on-netreg/internal/technitium"
	"github.com/Ohgwen/on-netreg/internal/unifi"
)

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
	DB     *gorm.DB
	Unifi  UnifiClient
	DNS    DNSClient
	Config config.Config
	Logger *slog.Logger
}

// Run blocks, running one reconciliation cycle immediately and then on
// every tick of Config.Unifi.PollInterval, until ctx is canceled.
func (e *Engine) Run(ctx context.Context) {
	e.runOnceLogged(ctx)

	ticker := time.NewTicker(e.Config.Unifi.PollInterval)
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

// RunOnce performs a single fetch-reconcile-apply cycle.
func (e *Engine) RunOnce(ctx context.Context) error {
	var existing []db.Device
	if err := e.DB.Find(&existing).Error; err != nil {
		return err
	}

	seen, err := e.Unifi.FetchClients(ctx)
	if err != nil {
		return err
	}

	now := time.Now()
	result := registry.Reconcile(now, existing, seen, e.Config.DNS)

	outcomes := e.applyChanges(ctx, result.Changes)

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

func (e *Engine) applyChanges(ctx context.Context, changes []registry.Change) map[string]applyOutcome {
	outcomes := make(map[string]applyOutcome, len(changes))
	zone := e.Config.Technitium.Zone

	for _, change := range changes {
		var applyErr error

		switch change.Kind {
		case registry.ChangeCreate:
			applyErr = e.DNS.AddRecord(ctx, technitium.AddRecordRequest{
				Domain:        fqdn(change.Hostname, zone),
				Zone:          zone,
				Type:          "A",
				TTL:           e.Config.Technitium.TTL,
				IPAddress:     change.IPAddress,
				Overwrite:     true,
				PTR:           e.Config.Technitium.CreatePTR,
				CreatePTRZone: e.Config.Technitium.CreatePTR,
			})
		case registry.ChangeUpdate:
			applyErr = e.DNS.UpdateRecord(ctx, technitium.UpdateRecordRequest{
				Domain:       fqdn(change.PreviousHostname, zone),
				Zone:         zone,
				Type:         "A",
				IPAddress:    change.PreviousIPAddress,
				NewDomain:    fqdn(change.Hostname, zone),
				NewIPAddress: change.IPAddress,
				NewTTL:       e.Config.Technitium.TTL,
			})
		case registry.ChangeDelete:
			applyErr = e.DNS.DeleteRecord(ctx, technitium.DeleteRecordRequest{
				Domain:    fqdn(change.PreviousHostname, zone),
				Zone:      zone,
				Type:      "A",
				IPAddress: change.PreviousIPAddress,
			})
		}

		outcome := applyOutcome{synced: applyErr == nil}
		if applyErr != nil {
			outcome.errMsg = applyErr.Error()
			if change.Kind == registry.ChangeDelete {
				// Record deletion failed; treat it as still present.
				outcome.synced = true
			}
			e.Logger.Error("failed to apply DNS change", "mac", change.MAC, "kind", change.Kind, "error", applyErr)
		} else {
			e.Logger.Info("applied DNS change", "mac", change.MAC, "kind", change.Kind, "hostname", change.Hostname)
		}
		outcomes[change.MAC] = outcome

		event := db.SyncEvent{
			MAC:       change.MAC,
			Action:    db.SyncEventAction(change.Kind),
			Success:   applyErr == nil,
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
