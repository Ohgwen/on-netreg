package db

import "time"

// Device is a network client tracked by the registry, keyed by MAC address.
type Device struct {
	ID uint `gorm:"primaryKey"`

	// MAC is normalized to lowercase colon-separated form, e.g. aa:bb:cc:dd:ee:ff.
	MAC string `gorm:"uniqueIndex;size:17;not null"`

	// Hostname is the DNS label currently in effect (after override/fallback resolution).
	Hostname string `gorm:"size:63;not null"`
	// UniFiName is the raw name/hostname UniFi reported for this client, if any.
	UniFiName string `gorm:"size:255"`
	// OverrideHostname, when set, takes precedence over the derived hostname.
	OverrideHostname *string `gorm:"size:63"`

	IPAddress string `gorm:"size:45"`

	// ControllerID is the UnifiController this device was last seen on.
	ControllerID uint `gorm:"not null;index"`

	// IdentityID, when set, means this device's MAC is a member of an
	// Identity: another MAC address speaks for it in DNS, and this device's
	// own record is intentionally never created/updated by the sync engine
	// (see internal/registry.Reconcile's skipDNS handling). Denormalized
	// from IdentityMember for cheap lookups from the dashboard/device pages.
	IdentityID *uint `gorm:"index"`
	// NetworkID, if set, is the UnifiNetwork (VLAN) this device was last
	// seen on. Nil when the client's network hasn't been mapped/refreshed
	// yet.
	NetworkID *uint
	// UniFiNetworkName is the raw network name as reported by the
	// controller, kept for display even when NetworkID is nil.
	UniFiNetworkName string

	// Zone is the DNS zone the device's current DNS record actually lives
	// in (mirrors Hostname/IPAddress as "current synced state"), so a later
	// zone-mapping change can still locate and update/delete the old
	// record.
	Zone string

	// IsFixedIP reflects whether UniFi reports this client with a static
	// IP assignment, as opposed to a DHCP-leased one.
	IsFixedIP bool
	// LeaseEstimatedExpiry is a best-effort estimate (LastSeen + the
	// client's network's configured DHCP lease time) for dynamic clients.
	// UniFi's local API does not reliably expose the true lease expiry, so
	// this is nil whenever the lease time isn't known.
	LeaseEstimatedExpiry *time.Time

	// Excluded devices are tracked but never synced to DNS.
	Excluded bool `gorm:"not null;default:false"`

	// DNSRecordSynced reflects whether the current Hostname/IPAddress pair
	// has been successfully written to Technitium.
	DNSRecordSynced bool `gorm:"not null;default:false"`
	LastSyncError   string

	FirstSeen time.Time `gorm:"not null"`
	LastSeen  time.Time `gorm:"not null;index"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// EffectiveHostname returns the OverrideHostname if set, else Hostname.
func (d Device) EffectiveHostname() string {
	if d.OverrideHostname != nil && *d.OverrideHostname != "" {
		return *d.OverrideHostname
	}
	return d.Hostname
}

// OverrideValue returns the override hostname as a plain string, or "" if
// none is set. Templates can't dereference a *string directly.
func (d Device) OverrideValue() string {
	if d.OverrideHostname != nil {
		return *d.OverrideHostname
	}
	return ""
}

type SyncEventAction string

const (
	SyncEventCreate SyncEventAction = "create"
	SyncEventUpdate SyncEventAction = "update"
	SyncEventDelete SyncEventAction = "delete"
	SyncEventError  SyncEventAction = "error"

	// Admin-driven actions, logged from the dashboard/settings handlers
	// rather than the sync engine.
	SyncEventOverride       SyncEventAction = "override"
	SyncEventExclude        SyncEventAction = "exclude"
	SyncEventInclude        SyncEventAction = "include"
	SyncEventForget         SyncEventAction = "forget"
	SyncEventManualSync     SyncEventAction = "manual_sync"
	SyncEventSettingsChange SyncEventAction = "settings_change"
)

// SystemActor identifies sync-engine-driven events, as opposed to a
// specific logged-in user's display name/email.
const SystemActor = "system"

// SyncEvent is an append-only audit log entry: either a sync-loop action
// taken against a device's DNS record, or an admin action taken from the
// dashboard/settings UI. Together these form the unified audit log,
// filterable by device (MAC) or by user (Actor).
type SyncEvent struct {
	ID uint `gorm:"primaryKey"`

	DeviceID *uint
	MAC      string `gorm:"size:17;index"`

	Action  SyncEventAction `gorm:"size:32;not null"`
	Detail  string
	Success bool `gorm:"not null"`

	// Actor is SystemActor for sync-engine events, or the logged-in user's
	// display name/email for actions taken from the webapp.
	Actor string `gorm:"size:255;index;not null;default:system"`

	CreatedAt time.Time `gorm:"index"`
}
