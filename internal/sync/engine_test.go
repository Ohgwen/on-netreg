package sync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"gorm.io/gorm"

	"github.com/Ohgwen/on-netreg/internal/config"
	"github.com/Ohgwen/on-netreg/internal/db"
	"github.com/Ohgwen/on-netreg/internal/settings"
	"github.com/Ohgwen/on-netreg/internal/technitium"
	"github.com/Ohgwen/on-netreg/internal/unifi"
)

type fakeUnifi struct {
	clients []unifi.NetworkClient
	err     error
}

func (f fakeUnifi) FetchClients(ctx context.Context) ([]unifi.NetworkClient, error) {
	return f.clients, f.err
}

type fakeDNS struct {
	addCalls    []technitium.AddRecordRequest
	updateCalls []technitium.UpdateRecordRequest
	deleteCalls []technitium.DeleteRecordRequest

	addErr, updateErr, deleteErr error
}

func (f *fakeDNS) AddRecord(ctx context.Context, r technitium.AddRecordRequest) error {
	f.addCalls = append(f.addCalls, r)
	return f.addErr
}

func (f *fakeDNS) UpdateRecord(ctx context.Context, r technitium.UpdateRecordRequest) error {
	f.updateCalls = append(f.updateCalls, r)
	return f.updateErr
}

func (f *fakeDNS) DeleteRecord(ctx context.Context, r technitium.DeleteRecordRequest) error {
	f.deleteCalls = append(f.deleteCalls, r)
	return f.deleteErr
}

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	// Each test gets its own named in-memory database so state from one
	// test doesn't leak into the next via a shared cache.
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	gdb, err := db.Open(config.DatabaseConfig{Driver: "sqlite", DSN: dsn})
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	return gdb
}

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func seedAppSettings(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	if err := gdb.Create(&db.AppSettings{ID: 1, FallbackPattern: "{vendor}-{macsuffix}"}).Error; err != nil {
		t.Fatalf("seeding app settings: %v", err)
	}
}

func seedTechnitium(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	if err := gdb.Create(&db.TechnitiumSettings{ID: 1}).Error; err != nil {
		t.Fatalf("seeding technitium settings: %v", err)
	}
}

func seedController(t *testing.T, gdb *gorm.DB, defaultZone string) db.UnifiController {
	t.Helper()
	ctrl := db.UnifiController{
		Name:        "default",
		BaseURL:     "https://udm.local",
		Site:        "default",
		DefaultZone: defaultZone,
		Enabled:     true,
	}
	if err := gdb.Create(&ctrl).Error; err != nil {
		t.Fatalf("seeding controller: %v", err)
	}
	return ctrl
}

func newTestEngine(gdb *gorm.DB, dns DNSClient, uc UnifiClient) *Engine {
	return &Engine{
		DB:        gdb,
		Logger:    silentLogger(),
		SecretKey: settings.Key("test-secret"),
		NewUnifiClient: func(config.UnifiConfig) (UnifiClient, error) {
			return uc, nil
		},
		NewDNSClient: func(config.TechnitiumConfig) DNSClient {
			return dns
		},
	}
}

func TestRunOnceCreatesDeviceAndDNSRecord(t *testing.T) {
	gdb := testDB(t)
	seedAppSettings(t, gdb)
	seedTechnitium(t, gdb)
	seedController(t, gdb, "lan.example.com")

	dns := &fakeDNS{}
	uc := fakeUnifi{clients: []unifi.NetworkClient{{MAC: "aa:bb:cc:dd:ee:01", Name: "Laptop", IP: "192.168.1.100"}}}
	e := newTestEngine(gdb, dns, uc)

	if err := e.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	var devices []db.Device
	if err := gdb.Find(&devices).Error; err != nil {
		t.Fatalf("querying devices: %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("expected 1 device, got %d", len(devices))
	}
	if devices[0].Hostname != "laptop" || !devices[0].DNSRecordSynced {
		t.Errorf("unexpected device state: %+v", devices[0])
	}
	if devices[0].Zone != "lan.example.com" {
		t.Errorf("zone = %q, want lan.example.com", devices[0].Zone)
	}

	if len(dns.addCalls) != 1 {
		t.Fatalf("expected 1 AddRecord call, got %d", len(dns.addCalls))
	}
	if dns.addCalls[0].Domain != "laptop.lan.example.com" {
		t.Errorf("domain = %q, want laptop.lan.example.com", dns.addCalls[0].Domain)
	}
}

func TestRunOnceMarksDeviceUnsyncedOnDNSFailure(t *testing.T) {
	gdb := testDB(t)
	seedAppSettings(t, gdb)
	seedTechnitium(t, gdb)
	seedController(t, gdb, "lan.example.com")

	dns := &fakeDNS{addErr: errors.New("technitium unreachable")}
	uc := fakeUnifi{clients: []unifi.NetworkClient{{MAC: "aa:bb:cc:dd:ee:01", Name: "Laptop", IP: "192.168.1.100"}}}
	e := newTestEngine(gdb, dns, uc)

	if err := e.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	var devices []db.Device
	if err := gdb.Find(&devices).Error; err != nil {
		t.Fatalf("querying devices: %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("expected 1 device, got %d", len(devices))
	}
	if devices[0].DNSRecordSynced {
		t.Errorf("expected DNSRecordSynced=false after DNS error")
	}
	if devices[0].LastSyncError == "" {
		t.Errorf("expected LastSyncError to be set")
	}

	var events []db.SyncEvent
	if err := gdb.Find(&events).Error; err != nil {
		t.Fatalf("querying sync events: %v", err)
	}
	if len(events) != 1 || events[0].Success {
		t.Fatalf("expected 1 failed sync event, got %+v", events)
	}
	if events[0].Actor != db.SystemActor {
		t.Errorf("actor = %q, want %q", events[0].Actor, db.SystemActor)
	}
}

func TestRunOnceSkipsExcludedDevice(t *testing.T) {
	gdb := testDB(t)
	seedAppSettings(t, gdb)
	seedTechnitium(t, gdb)
	ctrl := seedController(t, gdb, "lan.example.com")

	if err := gdb.Create(&db.Device{MAC: "aa:bb:cc:dd:ee:01", ControllerID: ctrl.ID, Hostname: "laptop", Zone: "lan.example.com", Excluded: true}).Error; err != nil {
		t.Fatalf("seeding device: %v", err)
	}

	dns := &fakeDNS{}
	uc := fakeUnifi{clients: []unifi.NetworkClient{{MAC: "aa:bb:cc:dd:ee:01", Name: "Laptop", IP: "192.168.1.100"}}}
	e := newTestEngine(gdb, dns, uc)

	if err := e.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if len(dns.addCalls) != 0 || len(dns.updateCalls) != 0 {
		t.Errorf("expected no DNS calls for excluded device, got add=%d update=%d", len(dns.addCalls), len(dns.updateCalls))
	}
}

func TestRunOnceTakesDownAndRecreatesRecordAsIPComesAndGoes(t *testing.T) {
	gdb := testDB(t)
	seedAppSettings(t, gdb)
	seedTechnitium(t, gdb)
	seedController(t, gdb, "lan.example.com")

	dns := &fakeDNS{}
	uc := &mutableUnifi{clients: []unifi.NetworkClient{{MAC: "aa:bb:cc:dd:ee:01", Name: "Laptop", IP: "192.168.1.100"}}}
	e := newTestEngine(gdb, dns, uc)

	// Cycle 1: device has an IP, gets created and marked synced.
	if err := e.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce (create): %v", err)
	}
	var dev db.Device
	if err := gdb.First(&dev).Error; err != nil {
		t.Fatalf("querying device: %v", err)
	}
	if !dev.DNSRecordSynced || len(dns.addCalls) != 1 {
		t.Fatalf("expected device synced after create, got synced=%v addCalls=%d", dev.DNSRecordSynced, len(dns.addCalls))
	}

	// Cycle 2: the client loses its IP -- the record should come down and
	// DNSRecordSynced should flip to false (not stay true, which would
	// silently leave the stale record in Technitium forever).
	uc.clients[0].IP = ""
	if err := e.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce (delete): %v", err)
	}
	if err := gdb.First(&dev).Error; err != nil {
		t.Fatalf("querying device: %v", err)
	}
	if dev.DNSRecordSynced {
		t.Errorf("expected DNSRecordSynced=false after the record was taken down")
	}
	if len(dns.deleteCalls) != 1 {
		t.Fatalf("expected 1 DeleteRecord call, got %d", len(dns.deleteCalls))
	}

	// Cycle 3: the IP comes back -- the record should be (re)created, not
	// "updated" against a record that no longer exists.
	uc.clients[0].IP = "192.168.1.100"
	if err := e.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce (recreate): %v", err)
	}
	if err := gdb.First(&dev).Error; err != nil {
		t.Fatalf("querying device: %v", err)
	}
	if !dev.DNSRecordSynced {
		t.Errorf("expected DNSRecordSynced=true after the record was recreated")
	}
	if len(dns.addCalls) != 2 {
		t.Fatalf("expected a 2nd AddRecord call to recreate the record, got %d total", len(dns.addCalls))
	}
	if len(dns.updateCalls) != 0 {
		t.Errorf("expected no UpdateRecord calls (nothing to update -- the record was gone), got %d", len(dns.updateCalls))
	}
}

type mutableUnifi struct {
	clients []unifi.NetworkClient
}

func (m *mutableUnifi) FetchClients(ctx context.Context) ([]unifi.NetworkClient, error) {
	return m.clients, nil
}

func TestRunOnceNoOpWithNoControllers(t *testing.T) {
	gdb := testDB(t)
	seedAppSettings(t, gdb)
	seedTechnitium(t, gdb)

	dns := &fakeDNS{}
	uc := fakeUnifi{}
	e := newTestEngine(gdb, dns, uc)

	if err := e.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(dns.addCalls) != 0 {
		t.Errorf("expected no DNS calls with no controllers configured")
	}
}
