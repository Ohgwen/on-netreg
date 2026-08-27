// Package config loads netreg's configuration from a YAML file with
// environment variable overrides.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Database DatabaseConfig `yaml:"database"`
	// Unifi and Technitium are one-time seed values only: on first startup,
	// if their respective DB tables are empty, these populate the DB-backed
	// settings (see internal/settings). After that the DB is authoritative
	// and editing these sections has no further effect -- manage
	// controllers/DNS connection settings from the webapp instead.
	Unifi      UnifiConfig      `yaml:"unifi"`
	Technitium TechnitiumConfig `yaml:"technitium"`
	// DNS is likewise a one-time seed for the DB-backed AppSettings row.
	DNS    DNSConfig    `yaml:"dns"`
	OIDC   OIDCConfig   `yaml:"oidc"`
	Server ServerConfig `yaml:"server"`
}

type DatabaseConfig struct {
	// Driver is "sqlite" or "postgres".
	Driver string `yaml:"driver"`
	// DSN is the sqlite file path or the postgres connection string.
	DSN string `yaml:"dsn"`
}

type UnifiConfig struct {
	BaseURL             string        `yaml:"base_url"`
	Username            string        `yaml:"username"`
	Password            string        `yaml:"password"`
	Site                string        `yaml:"site"`
	InsecureSkipVerify  bool          `yaml:"insecure_skip_verify"`
	PollInterval        time.Duration `yaml:"poll_interval"`
}

type TechnitiumConfig struct {
	BaseURL  string `yaml:"base_url"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	// Zone is a one-time seed for AppSettings.DefaultZone (see
	// internal/settings) -- it is not read again after the DB row exists.
	Zone      string `yaml:"zone"`
	TTL       int    `yaml:"ttl"`
	CreatePTR bool   `yaml:"create_ptr"`
}

type DNSConfig struct {
	// FallbackPattern generates a hostname for clients with no usable
	// UniFi name. Supports {vendor} and {macsuffix} placeholders.
	FallbackPattern string `yaml:"fallback_pattern"`
	// RemoveAfterAbsenceDays, when > 0, deletes a device's DNS record once
	// it hasn't been seen for this many days. 0 disables auto-removal.
	RemoveAfterAbsenceDays int `yaml:"remove_after_absence_days"`
}

type OIDCConfig struct {
	IssuerURL    string   `yaml:"issuer_url"`
	ClientID     string   `yaml:"client_id"`
	ClientSecret string   `yaml:"client_secret"`
	RedirectURL  string   `yaml:"redirect_url"`
	Scopes       []string `yaml:"scopes"`
	// GroupsClaim is the ID token claim holding the user's group
	// memberships, used to decide access to the admin-only Settings area.
	GroupsClaim string `yaml:"groups_claim"`
	// AdminGroup is the group name that grants Settings access. When empty,
	// every authenticated user is treated as admin (matches this app's
	// current behavior for anyone who doesn't configure it).
	AdminGroup string `yaml:"admin_group"`
}

type ServerConfig struct {
	ListenAddr string `yaml:"listen_addr"`
	// AuthEnabled gates OIDC login. When false, the dashboard is served with
	// no authentication at all -- only for local development/testing on a
	// trusted network, never for anything internet-reachable.
	AuthEnabled bool `yaml:"auth_enabled"`
	// SessionSecret signs OIDC session cookies and, via internal/settings,
	// derives the key used to encrypt UniFi/Technitium passwords stored in
	// the database. Required even when auth is disabled.
	SessionSecret string `yaml:"session_secret"`
}

func Defaults() Config {
	return Config{
		Database: DatabaseConfig{
			Driver: "sqlite",
			DSN:    "/data/netreg.db",
		},
		Unifi: UnifiConfig{
			Site:         "default",
			PollInterval: 60 * time.Second,
		},
		Technitium: TechnitiumConfig{
			TTL: 3600,
		},
		DNS: DNSConfig{
			FallbackPattern: "{vendor}-{macsuffix}",
		},
		OIDC: OIDCConfig{
			Scopes:      []string{"openid", "profile", "email"},
			GroupsClaim: "groups",
		},
		Server: ServerConfig{
			ListenAddr:  ":8080",
			AuthEnabled: true,
		},
	}
}

// Load reads the YAML config file at path (if it exists), then applies
// NETREG_-prefixed environment variable overrides on top.
func Load(path string) (Config, error) {
	cfg := Defaults()

	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				return Config{}, fmt.Errorf("reading config file: %w", err)
			}
		} else if err := yaml.Unmarshal(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("parsing config file: %w", err)
		}
	}

	applyEnvOverrides(&cfg)

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func (c Config) Validate() error {
	var problems []string

	switch c.Database.Driver {
	case "sqlite", "postgres":
	default:
		problems = append(problems, fmt.Sprintf("database.driver must be 'sqlite' or 'postgres', got %q", c.Database.Driver))
	}
	if c.Database.DSN == "" {
		problems = append(problems, "database.dsn is required")
	}
	if c.Server.SessionSecret == "" {
		problems = append(problems, "server.session_secret is required (used for OIDC sessions and to encrypt stored connection settings)")
	}
	if c.Server.AuthEnabled {
		if c.OIDC.IssuerURL == "" {
			problems = append(problems, "oidc.issuer_url is required (or set server.auth_enabled: false)")
		}
		if c.OIDC.ClientID == "" {
			problems = append(problems, "oidc.client_id is required (or set server.auth_enabled: false)")
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("invalid config:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

// applyEnvOverrides overrides config fields from environment variables of
// the form NETREG_SECTION_FIELD, e.g. NETREG_UNIFI_BASE_URL.
func applyEnvOverrides(cfg *Config) {
	str := func(env string, dst *string) {
		if v, ok := os.LookupEnv(env); ok {
			*dst = v
		}
	}
	boolean := func(env string, dst *bool) {
		if v, ok := os.LookupEnv(env); ok {
			if b, err := strconv.ParseBool(v); err == nil {
				*dst = b
			}
		}
	}
	integer := func(env string, dst *int) {
		if v, ok := os.LookupEnv(env); ok {
			if i, err := strconv.Atoi(v); err == nil {
				*dst = i
			}
		}
	}
	duration := func(env string, dst *time.Duration) {
		if v, ok := os.LookupEnv(env); ok {
			if d, err := time.ParseDuration(v); err == nil {
				*dst = d
			}
		}
	}

	str("NETREG_DATABASE_DRIVER", &cfg.Database.Driver)
	str("NETREG_DATABASE_DSN", &cfg.Database.DSN)

	str("NETREG_UNIFI_BASE_URL", &cfg.Unifi.BaseURL)
	str("NETREG_UNIFI_USERNAME", &cfg.Unifi.Username)
	str("NETREG_UNIFI_PASSWORD", &cfg.Unifi.Password)
	str("NETREG_UNIFI_SITE", &cfg.Unifi.Site)
	boolean("NETREG_UNIFI_INSECURE_SKIP_VERIFY", &cfg.Unifi.InsecureSkipVerify)
	duration("NETREG_UNIFI_POLL_INTERVAL", &cfg.Unifi.PollInterval)

	str("NETREG_TECHNITIUM_BASE_URL", &cfg.Technitium.BaseURL)
	str("NETREG_TECHNITIUM_USERNAME", &cfg.Technitium.Username)
	str("NETREG_TECHNITIUM_PASSWORD", &cfg.Technitium.Password)
	str("NETREG_TECHNITIUM_ZONE", &cfg.Technitium.Zone)
	integer("NETREG_TECHNITIUM_TTL", &cfg.Technitium.TTL)
	boolean("NETREG_TECHNITIUM_CREATE_PTR", &cfg.Technitium.CreatePTR)

	str("NETREG_DNS_FALLBACK_PATTERN", &cfg.DNS.FallbackPattern)
	integer("NETREG_DNS_REMOVE_AFTER_ABSENCE_DAYS", &cfg.DNS.RemoveAfterAbsenceDays)

	str("NETREG_OIDC_ISSUER_URL", &cfg.OIDC.IssuerURL)
	str("NETREG_OIDC_CLIENT_ID", &cfg.OIDC.ClientID)
	str("NETREG_OIDC_CLIENT_SECRET", &cfg.OIDC.ClientSecret)
	str("NETREG_OIDC_REDIRECT_URL", &cfg.OIDC.RedirectURL)
	if v, ok := os.LookupEnv("NETREG_OIDC_SCOPES"); ok {
		cfg.OIDC.Scopes = strings.Split(v, ",")
	}
	str("NETREG_OIDC_GROUPS_CLAIM", &cfg.OIDC.GroupsClaim)
	str("NETREG_OIDC_ADMIN_GROUP", &cfg.OIDC.AdminGroup)

	str("NETREG_SERVER_LISTEN_ADDR", &cfg.Server.ListenAddr)
	boolean("NETREG_SERVER_AUTH_ENABLED", &cfg.Server.AuthEnabled)
	str("NETREG_SERVER_SESSION_SECRET", &cfg.Server.SessionSecret)
}
