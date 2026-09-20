package sync

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Ohgwen/on-netreg/internal/config"
	"github.com/Ohgwen/on-netreg/internal/db"
	"github.com/Ohgwen/on-netreg/internal/technitium"
)

// syncAliases runs after devices and identities. Each DeviceAlias should
// exist in DNS as a CNAME <label>.<zone> -> <hostname>.<zone> exactly while
// its device has a confirmed A record; otherwise it should not exist. The
// alias row's Synced* fields say what is in DNS now, so any difference
// between that and the desired state is resolved by removing the old CNAME
// and then adding the new one.
func (e *Engine) syncAliases(ctx context.Context, dns DNSClient, dnsCfg config.TechnitiumConfig) error {
	var aliases []db.DeviceAlias
	if err := e.DB.Find(&aliases).Error; err != nil {
		return fmt.Errorf("loading aliases: %w", err)
	}
	if len(aliases) == 0 {
		return nil
	}

	ids := make([]uint, 0, len(aliases))
	for _, a := range aliases {
		ids = append(ids, a.DeviceID)
	}
	var devices []db.Device
	if err := e.DB.Where("id IN ?", ids).Find(&devices).Error; err != nil {
		return fmt.Errorf("loading alias devices: %w", err)
	}
	deviceByID := make(map[uint]db.Device, len(devices))
	for _, d := range devices {
		deviceByID[d.ID] = d
	}

	for i := range aliases {
		a := &aliases[i]
		dev, found := deviceByID[a.DeviceID]

		present := found && dev.DNSRecordSynced && !dev.Excluded && dev.Zone != ""
		var name, target string
		if present {
			name = fqdn(a.Label, dev.Zone)
			target = fqdn(dev.Hostname, dev.Zone)
		}

		if a.Synced && (!present || name != a.SyncedName || target != a.SyncedTarget) {
			err := dns.DeleteRecord(ctx, technitium.DeleteRecordRequest{
				Domain: a.SyncedName,
				Zone:   a.SyncedZone,
				Type:   "CNAME",
			})
			if err != nil && !recordAlreadyGone(err) {
				e.aliasEvent(dev.MAC, "delete", a.SyncedName, a.SyncedTarget, err)
				a.LastSyncError = err.Error()
				if err := e.DB.Save(a).Error; err != nil {
					return err
				}
				continue
			}
			e.aliasEvent(dev.MAC, "delete", a.SyncedName, a.SyncedTarget, nil)
			a.Synced, a.SyncedName, a.SyncedTarget, a.SyncedZone = false, "", "", ""
		}

		if !found {
			// The device is gone (forgotten); its alias goes with it.
			if err := e.DB.Delete(a).Error; err != nil {
				return err
			}
			continue
		}

		a.LastSyncError = ""
		if present && !a.Synced {
			err := dns.AddRecord(ctx, technitium.AddRecordRequest{
				Domain:    name,
				Zone:      dev.Zone,
				Type:      "CNAME",
				TTL:       dnsCfg.TTL,
				CNAME:     target,
				Overwrite: true,
			})
			e.aliasEvent(dev.MAC, "create", name, target, err)
			if err != nil {
				a.LastSyncError = err.Error()
			} else {
				a.Synced, a.SyncedName, a.SyncedTarget, a.SyncedZone = true, name, target, dev.Zone
			}
		}
		if err := e.DB.Save(a).Error; err != nil {
			return err
		}
	}
	return nil
}

// recordAlreadyGone reports whether a delete failed only because the
// record isn't there (e.g. removed by hand in Technitium), which is the
// state the delete was after anyway.
func recordAlreadyGone(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "no such") || strings.Contains(s, "not exist") || strings.Contains(s, "not found")
}

func (e *Engine) aliasEvent(mac, kind, name, target string, applyErr error) {
	detail := fmt.Sprintf("CNAME %s -> %s", name, target)
	if applyErr != nil {
		e.Logger.Error("failed to apply CNAME change", "mac", mac, "kind", kind, "name", name, "error", applyErr)
		detail += ": " + applyErr.Error()
	}
	event := db.SyncEvent{
		MAC:       mac,
		Action:    db.SyncEventAction(kind),
		Detail:    detail,
		Success:   applyErr == nil,
		Actor:     db.SystemActor,
		CreatedAt: time.Now(),
	}
	if err := e.DB.Create(&event).Error; err != nil {
		e.Logger.Error("failed to record sync event", "error", err)
	}
}
