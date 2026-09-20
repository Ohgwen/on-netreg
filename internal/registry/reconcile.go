package registry

import (
	"net"
	"time"

	"github.com/Ohgwen/on-netreg/internal/config"
	"github.com/Ohgwen/on-netreg/internal/db"
	"github.com/Ohgwen/on-netreg/internal/macaddr"
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

	// Hostname/IPAddress/Zone are the desired new values (Create, Update).
	Hostname  string
	IPAddress string
	Zone      string

	// PreviousHostname/PreviousIPAddress/PreviousZone identify the existing
	// record to modify or remove (Update, Delete).
	PreviousHostname  string
	PreviousIPAddress string
	PreviousZone      string
}

// Result is the outcome of a reconciliation pass: the full set of device
// rows to persist, and the DNS changes needed to match them.
type Result struct {
	Devices []db.Device
	Changes []Change
}

// NetworkInfo is what the caller knows, from locally-stored UnifiNetwork
// mappings, about the network/VLAN a client is on.
type NetworkInfo struct {
	// NetworkID is the local UnifiNetwork row id, nil if the client's
	// network hasn't been mapped/refreshed yet.
	NetworkID *uint
	// Zone is the resolved target DNS zone: the network's mapped zone, or
	// the caller's controller/global fallback already applied.
	Zone string
	// LeaseSeconds is the network's configured DHCP lease time, 0 if
	// unknown.
	LeaseSeconds int
}

// ZoneResolver resolves the network/zone info for one seen client, given
// the caller's locally-stored UnifiNetwork mappings and fallback zones.
type ZoneResolver func(client unifi.NetworkClient) NetworkInfo

// Reconcile computes the desired registry state from the current set of
// UniFi clients (all belonging to controllerID) and the previously known
// devices. It is a pure function: no I/O, fully deterministic given its
// inputs, so it can be unit tested without a live UniFi controller or
// Technitium server.
//
// skipDNS is the set of MACs that belong to an Identity (see
// internal/db.Identity): their own per-MAC DNS record is never
// created/updated/deleted here, since another MAC speaks for them, but they
// are still tracked -- IP/LastSeen/network fields update every cycle exactly
// like any other device. A nil map means "no identity members".
func Reconcile(now time.Time, controllerID uint, existing []db.Device, seen []unifi.NetworkClient, cfg config.DNSConfig, resolveNetwork ZoneResolver, skipDNS map[string]bool) Result {
	existingByMAC := make(map[string]db.Device, len(existing))
	for _, d := range existing {
		existingByMAC[d.MAC] = d
	}

	taken := make(map[string]string, len(existing))
	for _, d := range existing {
		if !d.Excluded {
			taken[TakenKey(d.Zone, d.Hostname)] = d.MAC
		}
	}

	seenMACs := make(map[string]bool, len(seen))
	var result Result

	for _, client := range seen {
		seenMACs[client.MAC] = true
		existingDevice, found := existingByMAC[client.MAC]
		info := resolveNetwork(client)
		zone := info.Zone
		// An admin-pinned zone wins over whatever the client's network maps to.
		if found && existingDevice.ZoneOverride != "" {
			zone = existingDevice.ZoneOverride
		}

		hasValidIP := HasValidIP(client.IP)

		if found && (existingDevice.Excluded || skipDNS[client.MAC]) {
			dev := existingDevice
			dev.ControllerID = controllerID
			if hasValidIP {
				dev.IPAddress = client.IP
			}
			dev.UniFiName = uniFiDisplayName(client)
			dev.UniFiNetworkName = client.Network
			applyConnectionDetails(&dev, client)
			dev.NetworkID = info.NetworkID
			dev.IsFixedIP = client.IsFixedIP
			dev.LeaseEstimatedExpiry = estimateLeaseExpiry(now, client.IsFixedIP, info.LeaseSeconds)
			dev.LastSeen = now
			if dev.FirstSeen.IsZero() {
				dev.FirstSeen = now
			}
			result.Devices = append(result.Devices, dev)
			continue
		}

		var override *string
		if found {
			override = existingDevice.OverrideHostname
		}
		hostname := Disambiguate(Resolve(client, override, cfg.FallbackPattern), client.MAC, zone, taken)
		taken[TakenKey(zone, hostname)] = client.MAC

		if !found {
			// A newly-seen client with a locally administered ("private")
			// MAC address is almost always a randomized address generated
			// by the device's OS for privacy, reassigned on every
			// reconnect -- not a stable identity worth publishing to DNS.
			// Such devices are tracked like any other but start excluded
			// from sync; an admin can still explicitly include one via the
			// dashboard if they know it's actually stable.
			private := macaddr.IsPrivate(client.MAC)

			dev := db.Device{
				MAC:                  client.MAC,
				ControllerID:         controllerID,
				Hostname:             hostname,
				Zone:                 zone,
				UniFiName:            uniFiDisplayName(client),
				UniFiNetworkName:     client.Network,
				NetworkID:            info.NetworkID,
				IsFixedIP:            client.IsFixedIP,
				LeaseEstimatedExpiry: estimateLeaseExpiry(now, client.IsFixedIP, info.LeaseSeconds),
				FirstSeen:            now,
				LastSeen:             now,
				Excluded:             private,
			}
			applyConnectionDetails(&dev, client)
			// A client with no valid IP yet (still negotiating DHCP, or
			// disconnected) is tracked for visibility but never gets a DNS
			// record until it actually has an address. Identity members are
			// tracked (including their IP) exactly like any other device,
			// but never get their own DNS record -- another MAC speaks for
			// them.
			if hasValidIP {
				dev.IPAddress = client.IP
			}
			if hasValidIP && !skipDNS[client.MAC] && !private {
				result.Changes = append(result.Changes, Change{
					Kind:      ChangeCreate,
					MAC:       client.MAC,
					Hostname:  hostname,
					IPAddress: client.IP,
					Zone:      zone,
				})
			}
			result.Devices = append(result.Devices, dev)
			continue
		}

		dev := existingDevice
		dev.ControllerID = controllerID
		hasRecord := dev.DNSRecordSynced
		zoneChanged := dev.Zone != "" && dev.Zone != zone
		hostnameChanged := dev.Hostname != hostname
		ipChanged := dev.IPAddress != client.IP && hasValidIP

		switch {
		case !hasValidIP:
			// The device no longer has a usable address (disconnected, or
			// mid-DHCP-renewal): take the record down rather than leave a
			// stale A record pointing at its last known IP. Nothing to do
			// if there's no confirmed record to remove.
			if hasRecord {
				result.Changes = append(result.Changes, Change{
					Kind:              ChangeDelete,
					MAC:               client.MAC,
					PreviousHostname:  dev.Hostname,
					PreviousIPAddress: dev.IPAddress,
					PreviousZone:      dev.Zone,
				})
			}
		case !hasRecord:
			// No confirmed record currently exists (never created, or just
			// taken down above in an earlier cycle) -- create fresh rather
			// than "update" a record that isn't there.
			result.Changes = append(result.Changes, Change{
				Kind:      ChangeCreate,
				MAC:       client.MAC,
				Hostname:  hostname,
				IPAddress: client.IP,
				Zone:      zone,
			})
		case zoneChanged:
			// Technitium records live in a specific zone; moving a client
			// to a newly-mapped zone means deleting the old record and
			// creating a fresh one, not an in-place update.
			result.Changes = append(result.Changes,
				Change{
					Kind:              ChangeDelete,
					MAC:               client.MAC,
					PreviousHostname:  dev.Hostname,
					PreviousIPAddress: dev.IPAddress,
					PreviousZone:      dev.Zone,
				},
				Change{
					Kind:      ChangeCreate,
					MAC:       client.MAC,
					Hostname:  hostname,
					IPAddress: client.IP,
					Zone:      zone,
				},
			)
		case hostnameChanged || ipChanged:
			result.Changes = append(result.Changes, Change{
				Kind:              ChangeUpdate,
				MAC:               client.MAC,
				Hostname:          hostname,
				IPAddress:         client.IP,
				Zone:              zone,
				PreviousHostname:  dev.Hostname,
				PreviousIPAddress: dev.IPAddress,
				PreviousZone:      dev.Zone,
			})
		}

		dev.Hostname = hostname
		dev.Zone = zone
		dev.UniFiName = uniFiDisplayName(client)
		dev.UniFiNetworkName = client.Network
		applyConnectionDetails(&dev, client)
		dev.NetworkID = info.NetworkID
		dev.IsFixedIP = client.IsFixedIP
		dev.LeaseEstimatedExpiry = estimateLeaseExpiry(now, client.IsFixedIP, info.LeaseSeconds)
		// Once a record is taken down for having no valid address, don't
		// keep showing the stale IP as if it were still current.
		dev.IPAddress = ""
		if hasValidIP {
			dev.IPAddress = client.IP
		}
		dev.LastSeen = now
		// A device registered before it ever connected has no FirstSeen yet.
		if dev.FirstSeen.IsZero() {
			dev.FirstSeen = now
		}
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
				PreviousZone:      dev.Zone,
			})
			dev.DNSRecordSynced = false
		}
		result.Devices = append(result.Devices, dev)
	}

	return result
}

// HasValidIP reports whether ip is a non-empty, parseable IP address.
// Devices are only synced to DNS while they have one; a blank or malformed
// address (disconnected, mid-DHCP-negotiation, garbage from the API) takes
// any existing record down instead of leaving it pointed at a stale value.
func HasValidIP(ip string) bool {
	return ip != "" && net.ParseIP(ip) != nil
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

// estimateLeaseExpiry returns a best-effort estimated DHCP lease expiry for
// a dynamically-addressed client: LastSeen (i.e. now, since this is only
// called while the client is being seen) plus its network's configured
// lease time. UniFi's local API doesn't reliably expose the true lease
// expiry, so nil is returned whenever the lease time isn't known, or the
// client has a static/fixed IP instead of a DHCP lease.
func estimateLeaseExpiry(now time.Time, isFixedIP bool, leaseSeconds int) *time.Time {
	if isFixedIP || leaseSeconds <= 0 {
		return nil
	}
	expiry := now.Add(time.Duration(leaseSeconds) * time.Second)
	return &expiry
}

// applyConnectionDetails copies the extended, purely-informational UniFi
// connection fields (signal, radio, port, data usage, etc.) onto dev. These
// never affect DNS sync decisions, so refreshing them is safe on every
// branch of Reconcile, including the excluded/skipDNS passthrough.
func applyConnectionDetails(dev *db.Device, c unifi.NetworkClient) {
	dev.Essid = c.Essid
	dev.RadioProto = c.RadioProto
	dev.Signal = c.Signal
	dev.SwPort = c.SwPort
	dev.UptimeSeconds = c.UptimeSeconds
	dev.RxBytes = c.RxBytes
	dev.TxBytes = c.TxBytes
	dev.Blocked = c.Blocked
}

func uniFiDisplayName(c unifi.NetworkClient) string {
	if c.Name != "" {
		return c.Name
	}
	return c.Hostname
}
