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

func testConfig() config.Config {
	cfg := config.Defaults()
	cfg.Technitium.Zone = "lan.example.com"
	return cfg
}

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRunOnceCreatesDeviceAndDNSRecord(t *testing.T) {
	gdb := testDB(t)
	dns := &fakeDNS{}
	e := &Engine{
		DB:     gdb,
		Unifi:  fakeUnifi{clients: []unifi.NetworkClient{{MAC: "aa:bb:cc:dd:ee:01", Name: "Laptop", IP: "192.168.1.100"}}},
		DNS:    dns,
		Config: testConfig(),
		Logger: silentLogger(),
	}

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

	if len(dns.addCalls) != 1 {
		t.Fatalf("expected 1 AddRecord call, got %d", len(dns.addCalls))
	}
	if dns.addCalls[0].Domain != "laptop.lan.example.com" {
		t.Errorf("domain = %q, want laptop.lan.example.com", dns.addCalls[0].Domain)
	}
}

func TestRunOnceMarksDeviceUnsyncedOnDNSFailure(t *testing.T) {
	gdb := testDB(t)
	dns := &fakeDNS{addErr: errors.New("technitium unreachable")}
	e := &Engine{
		DB:     gdb,
		Unifi:  fakeUnifi{clients: []unifi.NetworkClient{{MAC: "aa:bb:cc:dd:ee:01", Name: "Laptop", IP: "192.168.1.100"}}},
		DNS:    dns,
		Config: testConfig(),
		Logger: silentLogger(),
	}

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
}

func TestRunOnceSkipsExcludedDevice(t *testing.T) {
	gdb := testDB(t)
	if err := gdb.Create(&db.Device{MAC: "aa:bb:cc:dd:ee:01", Hostname: "laptop", Excluded: true}).Error; err != nil {
		t.Fatalf("seeding device: %v", err)
	}

	dns := &fakeDNS{}
	e := &Engine{
		DB:     gdb,
		Unifi:  fakeUnifi{clients: []unifi.NetworkClient{{MAC: "aa:bb:cc:dd:ee:01", Name: "Laptop", IP: "192.168.1.100"}}},
		DNS:    dns,
		Config: testConfig(),
		Logger: silentLogger(),
	}

	if err := e.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if len(dns.addCalls) != 0 || len(dns.updateCalls) != 0 {
		t.Errorf("expected no DNS calls for excluded device, got add=%d update=%d", len(dns.addCalls), len(dns.updateCalls))
	}
}
