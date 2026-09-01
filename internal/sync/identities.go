package sync

import (
	"context"
	"fmt"
	"sort"
	"time"

	"gorm.io/gorm"

	"github.com/Ohgwen/on-netreg/internal/config"
	"github.com/Ohgwen/on-netreg/internal/db"
	"github.com/Ohgwen/on-netreg/internal/registry"
	"github.com/Ohgwen/on-netreg/internal/technitium"
)

// syncIdentities runs once per cycle, after every controller's per-MAC
// devices have been synced. For each Identity it picks whichever member MAC
// should currently back the shared DNS record (registry.SelectActive) and
// applies whatever create/update/delete is needed to match, reusing the
// same applyChanges/SyncEvent machinery as per-device sync. A successful
// create/update is followed by a DNS lookup against the Technitium server
// itself, confirming the record actually resolves as expected.
func (e *Engine) syncIdentities(ctx context.Context, dns DNSClient, dnsCfg config.TechnitiumConfig, appSettings db.AppSettings) error {
	var identities []db.Identity
	if err := e.DB.Find(&identities).Error; err != nil {
		return fmt.Errorf("loading identities: %w", err)
	}
	if len(identities) == 0 {
		return nil
	}

	var members []db.IdentityMember
	if err := e.DB.Order("priority asc").Find(&members).Error; err != nil {
		return fmt.Errorf("loading identity members: %w", err)
	}
	membersByIdentity := make(map[uint][]db.IdentityMember, len(identities))
	memberMACs := make([]string, 0, len(members))
	for _, m := range members {
		membersByIdentity[m.IdentityID] = append(membersByIdentity[m.IdentityID], m)
		memberMACs = append(memberMACs, m.MAC)
	}

	var deviceRows []db.Device
	if len(memberMACs) > 0 {
		if err := e.DB.Where("mac IN ?", memberMACs).Find(&deviceRows).Error; err != nil {
			return fmt.Errorf("loading identity member devices: %w", err)
		}
	}
	deviceByMAC := make(map[string]db.Device, len(deviceRows))
	for _, d := range deviceRows {
		deviceByMAC[d.MAC] = d
	}

	freshWindow := 2 * appSettings.PollInterval
	if freshWindow <= 0 {
		freshWindow = 2 * config.Defaults().Unifi.PollInterval
	}
	isAlive := e.isAliveFunc()
	dnsHost := technitium.HostFromBaseURL(dnsCfg.BaseURL)
	now := time.Now()

	var changes []registry.Change
	type pending struct {
		ident      db.Identity
		outcomeKey string // MAC to look up in applyChanges' outcomes; "" if no change was emitted
	}
	updates := make([]pending, 0, len(identities))

	for _, ident := range identities {
		if ident.Excluded {
			updates = append(updates, pending{ident: ident})
			continue
		}

		memberStates := make([]registry.MemberState, 0, len(membersByIdentity[ident.ID]))
		for _, m := range membersByIdentity[ident.ID] {
			dev, ok := deviceByMAC[m.MAC]
			if !ok {
				continue
			}
			memberStates = append(memberStates, registry.MemberState{
				MAC:       dev.MAC,
				IPAddress: dev.IPAddress,
				LastSeen:  dev.LastSeen,
				Priority:  m.Priority,
			})
		}
		sort.SliceStable(memberStates, func(i, j int) bool { return memberStates[i].Priority < memberStates[j].Priority })

		active, fellBack := registry.SelectActive(now, memberStates, freshWindow, isAlive)
		if fellBack {
			e.Logger.Warn("identity: no member passed liveness check, using highest-priority fresh candidate anyway", "identity", ident.Name, "mac", active.MAC)
		}

		hostname := ident.EffectiveHostname()
		change, next, outcomeKey := identityChange(ident, active, hostname)
		if change != nil {
			changes = append(changes, *change)
		}
		updates = append(updates, pending{ident: next, outcomeKey: outcomeKey})
	}

	outcomes := e.applyChanges(ctx, dns, dnsCfg, changes)

	for i := range updates {
		u := &updates[i]
		outcome, ok := outcomes[u.outcomeKey]
		if !ok || u.outcomeKey == "" {
			continue
		}
		u.ident.DNSRecordSynced = outcome.synced
		u.ident.LastSyncError = outcome.errMsg

		if outcome.synced && outcome.errMsg == "" {
			target := fqdn(u.ident.EffectiveHostname(), u.ident.Zone)
			verifyErr := e.verifyDNS(ctx, dnsHost, target, u.ident.IPAddress, dnsVerifyTimeout)
			verifiedAt := time.Now()
			u.ident.LastVerifiedAt = &verifiedAt
			if verifyErr != nil {
				u.ident.LastVerifyError = verifyErr.Error()
				e.Logger.Warn("identity DNS verification failed", "identity", u.ident.Name, "fqdn", target, "error", verifyErr)
			} else {
				u.ident.LastVerifyError = ""
			}
		}
	}

	return e.DB.Transaction(func(tx *gorm.DB) error {
		for i := range updates {
			if err := tx.Save(&updates[i].ident).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// identityChange decides what DNS change (if any) is needed to bring an
// Identity's record in line with its currently-selected active member, and
// returns the updated Identity row plus the MAC key to look up the
// resulting apply outcome under (empty if no change was emitted).
func identityChange(ident db.Identity, active *registry.MemberState, hostname string) (*registry.Change, db.Identity, string) {
	next := ident
	hasRecord := ident.DNSRecordSynced

	if active == nil {
		if !hasRecord {
			return nil, next, ""
		}
		change := &registry.Change{
			Kind:              registry.ChangeDelete,
			MAC:               ident.ActiveMAC,
			PreviousHostname:  ident.SyncedHostname,
			PreviousIPAddress: ident.IPAddress,
			PreviousZone:      ident.Zone,
		}
		key := ident.ActiveMAC
		next.ActiveMAC = ""
		next.IPAddress = ""
		next.SyncedHostname = ""
		return change, next, key
	}

	macChanged := ident.ActiveMAC != active.MAC
	ipChanged := ident.IPAddress != active.IPAddress
	hostnameChanged := ident.SyncedHostname != hostname

	var change *registry.Change
	switch {
	case !hasRecord:
		change = &registry.Change{
			Kind:      registry.ChangeCreate,
			MAC:       active.MAC,
			Hostname:  hostname,
			IPAddress: active.IPAddress,
			Zone:      ident.Zone,
		}
	case macChanged || hostnameChanged || ipChanged:
		change = &registry.Change{
			Kind:              registry.ChangeUpdate,
			MAC:               active.MAC,
			Hostname:          hostname,
			IPAddress:         active.IPAddress,
			Zone:              ident.Zone,
			PreviousHostname:  ident.SyncedHostname,
			PreviousIPAddress: ident.IPAddress,
			PreviousZone:      ident.Zone,
		}
	}

	next.ActiveMAC = active.MAC
	next.IPAddress = active.IPAddress
	next.SyncedHostname = hostname

	if change == nil {
		return nil, next, ""
	}
	return change, next, active.MAC
}
