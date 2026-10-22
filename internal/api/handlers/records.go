package handlers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Ohgwen/on-netreg/internal/db"
	"github.com/Ohgwen/on-netreg/internal/registry"
	"github.com/Ohgwen/on-netreg/internal/technitium"
)

// deviceRecord is one DNS record for a device as the server has it, next to
// what netreg expects there.
type deviceRecord struct {
	technitium.DNSRecord
	// Role says what the record is to the device: its address record, the
	// reverse (PTR) record for its address, or one of its aliases.
	Role string
	// Expected is the value netreg publishes at this name, blank when it
	// doesn't publish one (excluded device, identity member, ...).
	Expected string
	// Missing marks a placeholder row: netreg expects a record here but the
	// server has none.
	Missing bool
}

// Matches reports whether the record holds the value netreg expects. A
// record netreg has no expectation for always matches.
func (r deviceRecord) Matches() bool {
	return r.Expected == "" || strings.EqualFold(strings.TrimSuffix(r.Value(), "."), strings.TrimSuffix(r.Expected, "."))
}

// deviceRecordTimeout bounds the live lookups made to render a device page,
// so an unreachable DNS server only slows the page down a little.
const deviceRecordTimeout = 5 * time.Second

// deviceRecords fetches the device's live DNS records from Technitium: the
// address record at its published name, the PTR for its address, and each
// alias's CNAME. Lookups that find no zone are not errors -- they just mean
// there's no record -- but a failure to reach the server is returned.
func (h *Handlers) deviceRecords(ctx context.Context, dev db.Device, aliases []aliasView) ([]deviceRecord, error) {
	if h.DNS == nil || dev.Zone == "" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, deviceRecordTimeout)
	defer cancel()
	dns, err := h.DNS(ctx)
	if err != nil {
		return nil, err
	}

	publishes := !dev.Excluded && dev.IdentityID == nil
	var out []deviceRecord
	// lookup appends the records of type rtype at name, or a Missing row
	// when there are none and netreg expects expected there.
	lookup := func(name, zone, rtype, role, expected string) error {
		records, err := dns.GetRecords(ctx, name, zone, false)
		if err != nil && !isNoZone(err) {
			return err
		}
		found := false
		for _, rec := range records {
			if !strings.EqualFold(rec.Type, rtype) || !strings.EqualFold(strings.TrimSuffix(rec.Name, "."), name) {
				continue
			}
			found = true
			out = append(out, deviceRecord{DNSRecord: rec, Role: role, Expected: expected})
		}
		if !found && expected != "" {
			out = append(out, deviceRecord{DNSRecord: technitium.DNSRecord{Name: name, Type: rtype}, Role: role, Expected: expected, Missing: true})
		}
		return nil
	}

	name := dev.Hostname + "." + dev.Zone
	var expectedIP string
	if publishes && dev.DNSRecordSynced {
		expectedIP = dev.IPAddress
	}
	if err := lookup(name, dev.Zone, "A", "address", expectedIP); err != nil {
		return nil, fmt.Errorf("looking up %s: %w", name, err)
	}

	if registry.HasValidIP(dev.IPAddress) {
		if reverse, err := technitium.ReverseName(dev.IPAddress); err == nil {
			// Whether a PTR should exist depends on the Technitium "create
			// PTR" setting, so a missing one isn't flagged.
			var expected string
			if expectedIP != "" {
				expected = name
			}
			before := len(out)
			if err := lookup(reverse, "", "PTR", "reverse", ""); err != nil {
				return out, fmt.Errorf("looking up %s: %w", reverse, err)
			}
			for i := before; i < len(out); i++ {
				out[i].Expected = expected
			}
		}
	}

	for _, a := range aliases {
		if !a.Synced {
			continue
		}
		if err := lookup(a.SyncedName, a.SyncedZone, "CNAME", "alias", a.SyncedTarget); err != nil {
			return out, fmt.Errorf("looking up %s: %w", a.SyncedName, err)
		}
	}
	return out, nil
}

// isNoZone reports whether err is Technitium saying the name isn't inside
// any zone it hosts, which for a lookup just means "no records".
func isNoZone(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "no such zone") || strings.Contains(s, "zone was not found") || strings.Contains(s, "no authoritative zone")
}
