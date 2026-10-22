package db

import "time"

// UnifiController is one UniFi OS console (UDM, UDM-Pro, Cloud Gateway,
// classic controller) whose clients netreg pulls in and syncs to DNS.
// Credentials are stored encrypted; see internal/settings.
type UnifiController struct {
	ID uint `gorm:"primaryKey"`

	Name               string `gorm:"uniqueIndex;size:100;not null"`
	BaseURL            string `gorm:"not null"`
	Username           string
	PasswordEncrypted  string
	Site               string `gorm:"not null;default:default"`
	InsecureSkipVerify bool   `gorm:"not null;default:false"`

	// DefaultZone is used for any client whose network has no explicit
	// UnifiNetwork.Zone mapping.
	DefaultZone string

	Enabled bool `gorm:"not null;default:true"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

// UnifiNetwork is a network/VLAN netreg knows about, mapped to the DNS zone
// its clients should sync to. Most are imported from a UnifiController;
// Manual ones are added by an admin in netreg. Either way, a device belongs
// to whichever network's IPSubnet contains its address -- UniFi's own idea
// of which network a client is on is not trusted (see
// registry.NetworkMatcher).
type UnifiNetwork struct {
	ID uint `gorm:"primaryKey"`

	// ControllerID is the controller this network was imported from; 0 for
	// a Manual network.
	ControllerID uint `gorm:"not null;index:idx_unifi_network_controller_unifi_id,unique"`

	// UnifiNetworkID is the controller's own id (_id) for this network,
	// used to upsert on refresh without losing the user-assigned Zone. For
	// a Manual network it is a generated "manual:" id that only keeps the
	// unique index happy.
	UnifiNetworkID string `gorm:"size:64;not null;index:idx_unifi_network_controller_unifi_id,unique"`

	// Manual marks a network added in netreg rather than imported from a
	// controller. Manual networks win over imported ones with the same
	// subnet, and a controller refresh never touches them.
	Manual bool `gorm:"not null;default:false"`

	Name     string
	VLAN     int
	IPSubnet string

	// DHCPLeaseTimeSeconds, if known, is used to compute an estimated lease
	// expiry for devices on this network (LastSeen + this duration).
	DHCPLeaseTimeSeconds int

	// Zone is the DNS zone clients on this network sync to. Empty means
	// "unmapped" -- falls back to the controller's DefaultZone.
	Zone string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// TechnitiumSettings is the (singleton, ID=1) connection to the Technitium
// DNS server used to write records.
type TechnitiumSettings struct {
	ID uint `gorm:"primaryKey"`

	BaseURL           string
	Username          string
	PasswordEncrypted string
	TTL               int
	CreatePTR         bool
	// VerifyDNSServer, when set, is the host (or host:port) queried for the
	// post-write "does this actually resolve" check instead of the
	// Technitium server itself (derived from BaseURL).
	VerifyDNSServer string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// AppSettings is the (singleton, ID=1) set of global sync defaults,
// editable from the webapp's General settings page.
type AppSettings struct {
	ID uint `gorm:"primaryKey"`

	// DefaultZone is the last-resort fallback zone for any client whose
	// controller also has no DefaultZone set.
	DefaultZone string

	FallbackPattern        string
	RemoveAfterAbsenceDays int
	PollInterval           time.Duration

	CreatedAt time.Time
	UpdatedAt time.Time
}
