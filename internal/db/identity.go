package db

import "time"

// Identity groups multiple MAC addresses that represent the same logical
// device (e.g. a laptop's WiFi adapter and its VPN adapter) under a single
// DNS record. At any given sync cycle, whichever member MAC is chosen as
// active (see internal/registry.SelectActive) backs the record; the other
// members' own per-MAC DNS records are suppressed (internal/registry.Reconcile
// skipDNS).
type Identity struct {
	ID uint `gorm:"primaryKey"`

	// Name is the DNS hostname label, unless OverrideHostname is set.
	Name             string  `gorm:"size:63;not null"`
	OverrideHostname *string `gorm:"size:63"`

	// Zone is a fixed, admin-chosen DNS zone. Unlike a per-MAC Device, an
	// Identity's zone does not follow whichever site/network the active
	// member is currently on -- Technitium zones are distinct namespaces, so
	// a stable FQDN requires a stable zone.
	Zone string `gorm:"not null"`

	// Excluded identities are tracked but never synced to DNS.
	Excluded bool `gorm:"not null;default:false"`

	// ActiveMAC is the member MAC currently backing the DNS record, mirrored
	// here (alongside IPAddress/DNSRecordSynced) as "current synced state"
	// so a later change in the active member can locate and update/delete
	// the old record.
	ActiveMAC       string `gorm:"size:17"`
	IPAddress       string `gorm:"size:45"`
	// SyncedHostname is the hostname label currently reflected in the DNS
	// record, tracked separately from Name/OverrideHostname (the desired
	// label) so an admin renaming the identity is detected as a change to
	// apply, the same way Device.Hostname trails Resolve()'s output.
	SyncedHostname  string `gorm:"size:63"`
	DNSRecordSynced bool   `gorm:"not null;default:false"`
	LastSyncError   string

	// LastVerifiedAt/LastVerifyError record the outcome of the most recent
	// post-write DNS lookup confirming the record actually resolves as
	// expected (see internal/technitium.Verify).
	LastVerifiedAt  *time.Time
	LastVerifyError string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// EffectiveHostname returns the OverrideHostname if set, else Name.
func (i Identity) EffectiveHostname() string {
	if i.OverrideHostname != nil && *i.OverrideHostname != "" {
		return *i.OverrideHostname
	}
	return i.Name
}

// OverrideValue returns the override hostname as a plain string, or "" if
// none is set. Templates can't dereference a *string directly.
func (i Identity) OverrideValue() string {
	if i.OverrideHostname != nil {
		return *i.OverrideHostname
	}
	return ""
}

// IdentityMember is one MAC address belonging to an Identity, with a
// priority (lower = tried first) used by SelectActive to choose which
// member backs the shared DNS record when more than one is currently up.
type IdentityMember struct {
	ID uint `gorm:"primaryKey"`

	IdentityID uint   `gorm:"not null;index;uniqueIndex:idx_identity_member_mac"`
	MAC        string `gorm:"size:17;not null;uniqueIndex:idx_identity_member_mac"`

	Priority int    `gorm:"not null;default:0"`
	Label    string `gorm:"size:100"`

	CreatedAt time.Time
}
