package settings

import (
	"fmt"
	"testing"

	"gorm.io/gorm"

	"github.com/Ohgwen/on-netreg/internal/config"
	"github.com/Ohgwen/on-netreg/internal/db"
)

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	gdb, err := db.Open(config.DatabaseConfig{Driver: "sqlite", DSN: dsn})
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	return gdb
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := Key("a test session secret")

	ciphertext, err := Encrypt(key, "hunter2")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if ciphertext == "hunter2" {
		t.Fatalf("ciphertext should not equal plaintext")
	}

	plaintext, err := Decrypt(key, ciphertext)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if plaintext != "hunter2" {
		t.Errorf("got %q, want hunter2", plaintext)
	}
}

func TestEncryptEmptyStringRoundTrips(t *testing.T) {
	key := Key("a test session secret")

	ciphertext, err := Encrypt(key, "")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if ciphertext != "" {
		t.Errorf("expected empty ciphertext for empty plaintext, got %q", ciphertext)
	}

	plaintext, err := Decrypt(key, ciphertext)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if plaintext != "" {
		t.Errorf("expected empty plaintext, got %q", plaintext)
	}
}

func TestDecryptWrongKeyFails(t *testing.T) {
	ciphertext, err := Encrypt(Key("secret-one"), "hunter2")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := Decrypt(Key("secret-two"), ciphertext); err == nil {
		t.Errorf("expected decryption with the wrong key to fail")
	}
}

func TestSeedFromConfigCreatesController(t *testing.T) {
	gdb := testDB(t)
	key := Key("test-secret")
	cfg := config.Config{
		Unifi: config.UnifiConfig{
			BaseURL:  "https://udm.local",
			Username: "admin",
			Password: "hunter2",
			Site:     "default",
		},
		Technitium: config.TechnitiumConfig{
			BaseURL: "http://technitium:5380",
			Zone:    "lan.example.com",
		},
	}

	if err := SeedFromConfig(gdb, key, cfg); err != nil {
		t.Fatalf("SeedFromConfig: %v", err)
	}

	controllers, err := LoadControllers(gdb, key)
	if err != nil {
		t.Fatalf("LoadControllers: %v", err)
	}
	if len(controllers) != 1 {
		t.Fatalf("expected 1 controller, got %d", len(controllers))
	}
	if controllers[0].Config.Password != "hunter2" {
		t.Errorf("password = %q, want hunter2", controllers[0].Config.Password)
	}
	if controllers[0].DefaultZone != "lan.example.com" {
		t.Errorf("default zone = %q, want lan.example.com", controllers[0].DefaultZone)
	}

	app, err := LoadApp(gdb)
	if err != nil {
		t.Fatalf("LoadApp: %v", err)
	}
	if app.DefaultZone != "lan.example.com" {
		t.Errorf("app default zone = %q, want lan.example.com", app.DefaultZone)
	}
}

func TestSeedFromConfigIsNoOpWhenControllerExists(t *testing.T) {
	gdb := testDB(t)
	key := Key("test-secret")

	existing := db.UnifiController{Name: "manually-added", BaseURL: "https://manual.local", Enabled: true}
	if err := SaveController(gdb, key, &existing, "manual-pass"); err != nil {
		t.Fatalf("seeding existing controller: %v", err)
	}

	cfg := config.Config{
		Unifi: config.UnifiConfig{BaseURL: "https://udm.local", Password: "hunter2"},
	}
	if err := SeedFromConfig(gdb, key, cfg); err != nil {
		t.Fatalf("SeedFromConfig: %v", err)
	}

	controllers, err := LoadControllers(gdb, key)
	if err != nil {
		t.Fatalf("LoadControllers: %v", err)
	}
	if len(controllers) != 1 || controllers[0].Name != "manually-added" {
		t.Fatalf("expected the manually-added controller to remain untouched, got %+v", controllers)
	}
}
