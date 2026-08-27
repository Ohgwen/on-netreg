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
)

// SyncEvent is an append-only audit log entry for one sync-loop action
// taken against a device's DNS record.
type SyncEvent struct {
	ID uint `gorm:"primaryKey"`

	DeviceID *uint
	MAC      string `gorm:"size:17;index"`

	Action  SyncEventAction `gorm:"size:16;not null"`
	Detail  string
	Success bool `gorm:"not null"`

	CreatedAt time.Time `gorm:"index"`
}
