package registry

import (
	"time"

	"github.com/Ohgwen/on-netreg/internal/config"
	"github.com/Ohgwen/on-netreg/internal/db"
	"github.com/Ohgwen/on-netreg/internal/unifi"
)

type ChangeKind string

const (
	ChangeCreate ChangeKind = "create"
	ChangeUpdate ChangeKind = "update"
	ChangeDelete ChangeKind = "delete"
)

// Change is one DNS action that needs to be applied against Technitium to
// bring it in line with the registry's desired state.
type Change struct {
	Kind ChangeKind
	MAC  string

	// Hostname/IPAddress are the desired new values (Create, Update).
	Hostname  string
	IPAddress string

	// PreviousHostname/PreviousIPAddress identify the existing record to
	// modify or remove (Update, Delete).
	PreviousHostname  string
	PreviousIPAddress string
}

// Result is the outcome of a reconciliation pass: the full set of device
// rows to persist, and the DNS changes needed to match them.
type Result struct {
	Devices []db.Device
	Changes []Change
}

// Reconcile computes the desired registry state from the current set of
// UniFi clients and the previously known devices. It is a pure function:
// no I/O, fully deterministic given its inputs, so it can be unit tested
// without a live UniFi controller or Technitium server.
func Reconcile(now time.Time, existing []db.Device, seen []unifi.NetworkClient, cfg config.DNSConfig) Result {
	existingByMAC := make(map[string]db.Device, len(existing))
	for _, d := range existing {
		existingByMAC[d.MAC] = d
	}

	taken := make(map[string]string, len(existing))
	for _, d := range existing {
		if !d.Excluded {
			taken[d.Hostname] = d.MAC
		}
	}

	seenMACs := make(map[string]bool, len(seen))
	var result Result

	for _, client := range seen {
		seenMACs[client.MAC] = true
		existingDevice, found := existingByMAC[client.MAC]

		if found && existingDevice.Excluded {
			dev := existingDevice
			dev.IPAddress = client.IP
			dev.UniFiName = uniFiDisplayName(client)
			dev.LastSeen = now
			result.Devices = append(result.Devices, dev)
			continue
		}

		var override *string
		if found {
			override = existingDevice.OverrideHostname
		}
		hostname := Disambiguate(Resolve(client, override, cfg.FallbackPattern), client.MAC, taken)
		taken[hostname] = client.MAC

		if !found {
			result.Devices = append(result.Devices, db.Device{
				MAC:       client.MAC,
				Hostname:  hostname,
				UniFiName: uniFiDisplayName(client),
				IPAddress: client.IP,
				FirstSeen: now,
				LastSeen:  now,
			})
			result.Changes = append(result.Changes, Change{
				Kind:      ChangeCreate,
				MAC:       client.MAC,
				Hostname:  hostname,
				IPAddress: client.IP,
			})
			continue
		}

		dev := existingDevice
		hostnameChanged := dev.Hostname != hostname
		ipChanged := dev.IPAddress != client.IP && client.IP != ""
		if hostnameChanged || ipChanged || !dev.DNSRecordSynced {
			result.Changes = append(result.Changes, Change{
				Kind:              ChangeUpdate,
				MAC:               client.MAC,
				Hostname:          hostname,
				IPAddress:         client.IP,
				PreviousHostname:  dev.Hostname,
				PreviousIPAddress: dev.IPAddress,
			})
		}
		dev.Hostname = hostname
		dev.UniFiName = uniFiDisplayName(client)
		if client.IP != "" {
			dev.IPAddress = client.IP
		}
		dev.LastSeen = now
		result.Devices = append(result.Devices, dev)
	}

	for _, d := range existing {
		if seenMACs[d.MAC] {
			continue
		}
		dev := d
		if shouldRemove(dev, now, cfg) {
			result.Changes = append(result.Changes, Change{
				Kind:              ChangeDelete,
				MAC:               dev.MAC,
				PreviousHostname:  dev.Hostname,
				PreviousIPAddress: dev.IPAddress,
			})
			dev.DNSRecordSynced = false
		}
		result.Devices = append(result.Devices, dev)
	}

	return result
}

func shouldRemove(dev db.Device, now time.Time, cfg config.DNSConfig) bool {
	if dev.Excluded || !dev.DNSRecordSynced {
		return false
	}
	if cfg.RemoveAfterAbsenceDays <= 0 {
		return false
	}
	absence := time.Duration(cfg.RemoveAfterAbsenceDays) * 24 * time.Hour
	return now.Sub(dev.LastSeen) > absence
}

func uniFiDisplayName(c unifi.NetworkClient) string {
	if c.Name != "" {
		return c.Name
	}
	return c.Hostname
}
