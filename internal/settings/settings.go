// Package settings owns the DB-backed connection settings (UniFi
// controllers, the Technitium connection, discovered networks, and global
// sync defaults) that the webapp's Settings pages manage at runtime, in
// place of the old config.yaml-only setup. It also owns the at-rest
// encryption of the passwords stored in those tables.
package settings

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"

	"gorm.io/gorm"

	"github.com/Ohgwen/on-netreg/internal/config"
	"github.com/Ohgwen/on-netreg/internal/db"
	"github.com/Ohgwen/on-netreg/internal/unifi"
)

// Key derives the AES-256-GCM key used to encrypt stored connection
// passwords from the server's session secret.
func Key(sessionSecret string) []byte {
	sum := sha256.Sum256([]byte(sessionSecret))
	return sum[:]
}

// Encrypt returns plaintext encrypted with AES-256-GCM, base64-encoded with
// the nonce prepended. An empty plaintext encrypts to an empty string, so
// clearing a stored password round-trips cleanly.
func Encrypt(key []byte, plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generating nonce: %w", err)
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt reverses Encrypt.
func Decrypt(key []byte, ciphertext string) (string, error) {
	if ciphertext == "" {
		return "", nil
	}
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	data, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", fmt.Errorf("decoding ciphertext: %w", err)
	}
	if len(data) < gcm.NonceSize() {
		return "", errors.New("ciphertext too short")
	}
	nonce, sealed := data[:gcm.NonceSize()], data[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return "", fmt.Errorf("decrypting: %w", err)
	}
	return string(plaintext), nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("creating cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("creating GCM: %w", err)
	}
	return gcm, nil
}

// ControllerRuntime is a UnifiController with its password decrypted and
// shaped into a config.UnifiConfig ready to build a client from.
type ControllerRuntime struct {
	ID          uint
	Name        string
	Config      config.UnifiConfig
	DefaultZone string
}

// LoadControllers returns every enabled UniFi controller with its password
// decrypted.
func LoadControllers(gdb *gorm.DB, key []byte) ([]ControllerRuntime, error) {
	var rows []db.UnifiController
	if err := gdb.Where("enabled = ?", true).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("loading unifi controllers: %w", err)
	}

	out := make([]ControllerRuntime, 0, len(rows))
	for _, row := range rows {
		password, err := Decrypt(key, row.PasswordEncrypted)
		if err != nil {
			return nil, fmt.Errorf("decrypting password for controller %q: %w", row.Name, err)
		}
		out = append(out, ControllerRuntime{
			ID:   row.ID,
			Name: row.Name,
			Config: config.UnifiConfig{
				BaseURL:            row.BaseURL,
				Username:           row.Username,
				Password:           password,
				Site:               row.Site,
				InsecureSkipVerify: row.InsecureSkipVerify,
			},
			DefaultZone: row.DefaultZone,
		})
	}
	return out, nil
}

// SaveController creates or updates a controller row. When plainPassword is
// non-empty it is encrypted and stored; passing "" leaves the currently
// stored password untouched (so edit forms can omit it to keep the
// existing credential).
func SaveController(gdb *gorm.DB, key []byte, ctrl *db.UnifiController, plainPassword string) error {
	if plainPassword != "" {
		enc, err := Encrypt(key, plainPassword)
		if err != nil {
			return fmt.Errorf("encrypting controller password: %w", err)
		}
		ctrl.PasswordEncrypted = enc
	}
	if err := gdb.Save(ctrl).Error; err != nil {
		return fmt.Errorf("saving unifi controller: %w", err)
	}
	return nil
}

// LoadTechnitium returns the (singleton) Technitium connection with its
// password decrypted.
func LoadTechnitium(gdb *gorm.DB, key []byte) (config.TechnitiumConfig, error) {
	var row db.TechnitiumSettings
	if err := gdb.First(&row, 1).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return config.TechnitiumConfig{}, errors.New("technitium connection is not configured -- set it up under Settings")
		}
		return config.TechnitiumConfig{}, fmt.Errorf("loading technitium settings: %w", err)
	}
	password, err := Decrypt(key, row.PasswordEncrypted)
	if err != nil {
		return config.TechnitiumConfig{}, fmt.Errorf("decrypting technitium password: %w", err)
	}
	return config.TechnitiumConfig{
		BaseURL:   row.BaseURL,
		Username:  row.Username,
		Password:  password,
		TTL:       row.TTL,
		CreatePTR: row.CreatePTR,
	}, nil
}

// SaveTechnitium creates or updates the singleton Technitium settings row.
// As with SaveController, an empty plainPassword leaves the stored
// password untouched.
func SaveTechnitium(gdb *gorm.DB, key []byte, row *db.TechnitiumSettings, plainPassword string) error {
	row.ID = 1
	if plainPassword != "" {
		enc, err := Encrypt(key, plainPassword)
		if err != nil {
			return fmt.Errorf("encrypting technitium password: %w", err)
		}
		row.PasswordEncrypted = enc
	}
	if err := gdb.Save(row).Error; err != nil {
		return fmt.Errorf("saving technitium settings: %w", err)
	}
	return nil
}

// LoadApp returns the singleton AppSettings row, creating it with library
// defaults if it doesn't exist yet (normally SeedFromConfig has already
// created it from config.yaml on first startup; this is a safety net).
func LoadApp(gdb *gorm.DB) (db.AppSettings, error) {
	var row db.AppSettings
	err := gdb.First(&row, 1).Error
	if err == nil {
		return row, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return db.AppSettings{}, fmt.Errorf("loading app settings: %w", err)
	}

	defaults := config.Defaults()
	row = db.AppSettings{
		ID:                     1,
		FallbackPattern:        defaults.DNS.FallbackPattern,
		RemoveAfterAbsenceDays: defaults.DNS.RemoveAfterAbsenceDays,
		PollInterval:           defaults.Unifi.PollInterval,
	}
	if err := gdb.Create(&row).Error; err != nil {
		return db.AppSettings{}, fmt.Errorf("creating default app settings: %w", err)
	}
	return row, nil
}

// SaveApp creates or updates the singleton AppSettings row.
func SaveApp(gdb *gorm.DB, row *db.AppSettings) error {
	row.ID = 1
	if err := gdb.Save(row).Error; err != nil {
		return fmt.Errorf("saving app settings: %w", err)
	}
	return nil
}

// LoadNetworks returns every discovered UniFi network across all
// controllers.
func LoadNetworks(gdb *gorm.DB) ([]db.UnifiNetwork, error) {
	var rows []db.UnifiNetwork
	if err := gdb.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("loading unifi networks: %w", err)
	}
	return rows, nil
}

// UpsertNetworks merges a freshly-fetched network list from a controller
// into the UnifiNetwork table, keyed by (controllerID, UniFi network id).
// Existing rows keep their user-assigned Zone; only the descriptive fields
// are refreshed.
func UpsertNetworks(gdb *gorm.DB, controllerID uint, fetched []unifi.Network) error {
	for _, n := range fetched {
		var existing db.UnifiNetwork
		err := gdb.Where("controller_id = ? AND unifi_network_id = ?", controllerID, n.ID).First(&existing).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			row := db.UnifiNetwork{
				ControllerID:         controllerID,
				UnifiNetworkID:       n.ID,
				Name:                 n.Name,
				VLAN:                 n.VLAN,
				IPSubnet:             n.IPSubnet,
				DHCPLeaseTimeSeconds: n.DHCPLeaseTimeSeconds,
			}
			if err := gdb.Create(&row).Error; err != nil {
				return fmt.Errorf("creating network %q: %w", n.Name, err)
			}
		case err != nil:
			return fmt.Errorf("looking up network %q: %w", n.Name, err)
		default:
			existing.Name = n.Name
			existing.VLAN = n.VLAN
			existing.IPSubnet = n.IPSubnet
			existing.DHCPLeaseTimeSeconds = n.DHCPLeaseTimeSeconds
			if err := gdb.Save(&existing).Error; err != nil {
				return fmt.Errorf("updating network %q: %w", n.Name, err)
			}
		}
	}
	return nil
}

// SeedFromConfig performs the one-time migration of config.yaml's unifi/
// technitium/dns sections into the DB-backed settings tables, on first
// startup only: each table is only seeded when it's still empty, so once a
// controller/connection/app-settings row exists (whether from a prior seed
// or created via the webapp), config.yaml has no further effect.
func SeedFromConfig(gdb *gorm.DB, key []byte, cfg config.Config) error {
	var controllerCount int64
	if err := gdb.Model(&db.UnifiController{}).Count(&controllerCount).Error; err != nil {
		return fmt.Errorf("counting unifi controllers: %w", err)
	}
	if controllerCount == 0 && cfg.Unifi.BaseURL != "" {
		ctrl := db.UnifiController{
			Name:               "default",
			BaseURL:            cfg.Unifi.BaseURL,
			Username:           cfg.Unifi.Username,
			Site:               cfg.Unifi.Site,
			InsecureSkipVerify: cfg.Unifi.InsecureSkipVerify,
			DefaultZone:        cfg.Technitium.Zone,
			Enabled:            true,
		}
		if err := SaveController(gdb, key, &ctrl, cfg.Unifi.Password); err != nil {
			return fmt.Errorf("seeding unifi controller from config: %w", err)
		}
	}

	var technitiumCount int64
	if err := gdb.Model(&db.TechnitiumSettings{}).Count(&technitiumCount).Error; err != nil {
		return fmt.Errorf("counting technitium settings: %w", err)
	}
	if technitiumCount == 0 && cfg.Technitium.BaseURL != "" {
		row := db.TechnitiumSettings{
			BaseURL:   cfg.Technitium.BaseURL,
			Username:  cfg.Technitium.Username,
			TTL:       cfg.Technitium.TTL,
			CreatePTR: cfg.Technitium.CreatePTR,
		}
		if err := SaveTechnitium(gdb, key, &row, cfg.Technitium.Password); err != nil {
			return fmt.Errorf("seeding technitium settings from config: %w", err)
		}
	}

	var appCount int64
	if err := gdb.Model(&db.AppSettings{}).Count(&appCount).Error; err != nil {
		return fmt.Errorf("counting app settings: %w", err)
	}
	if appCount == 0 {
		row := db.AppSettings{
			ID:                     1,
			DefaultZone:            cfg.Technitium.Zone,
			FallbackPattern:        cfg.DNS.FallbackPattern,
			RemoveAfterAbsenceDays: cfg.DNS.RemoveAfterAbsenceDays,
			PollInterval:           cfg.Unifi.PollInterval,
		}
		if err := gdb.Create(&row).Error; err != nil {
			return fmt.Errorf("seeding app settings from config: %w", err)
		}
	}

	return nil
}
