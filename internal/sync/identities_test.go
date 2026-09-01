package sync

import (
	"context"
	"testing"
	"time"

	"github.com/Ohgwen/on-netreg/internal/db"
)

func TestRunOnceIdentityPicksHighestPriorityMember(t *testing.T) {
	gdb := testDB(t)
	seedAppSettings(t, gdb)
	seedTechnitium(t, gdb)
	ctrl := seedController(t, gdb, "lan.example.com")

	now := time.Now()
	if err := gdb.Create(&db.Device{MAC: "aa:bb:cc:dd:ee:01", ControllerID: ctrl.ID, IPAddress: "192.168.1.10", LastSeen: now}).Error; err != nil {
		t.Fatalf("seeding device 1: %v", err)
	}
	if err := gdb.Create(&db.Device{MAC: "aa:bb:cc:dd:ee:02", ControllerID: ctrl.ID, IPAddress: "192.168.1.20", LastSeen: now}).Error; err != nil {
		t.Fatalf("seeding device 2: %v", err)
	}

	ident := db.Identity{Name: "laptop", Zone: "roam.example.com"}
	if err := gdb.Create(&ident).Error; err != nil {
		t.Fatalf("seeding identity: %v", err)
	}
	if err := gdb.Create(&db.IdentityMember{IdentityID: ident.ID, MAC: "aa:bb:cc:dd:ee:01", Priority: 0}).Error; err != nil {
		t.Fatalf("seeding member 1: %v", err)
	}
	if err := gdb.Create(&db.IdentityMember{IdentityID: ident.ID, MAC: "aa:bb:cc:dd:ee:02", Priority: 1}).Error; err != nil {
		t.Fatalf("seeding member 2: %v", err)
	}

	dns := &fakeDNS{}
	uc := fakeUnifi{}
	e := newTestEngine(gdb, dns, uc)
	e.NewIsAlive = func() func(ip string) bool { return func(string) bool { return true } }
	e.VerifyDNS = func(ctx context.Context, dnsHost, fqdn, expectedIP string, timeout time.Duration) error { return nil }

	if err := e.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if len(dns.addCalls) != 1 {
		t.Fatalf("expected 1 AddRecord call for the identity, got %d", len(dns.addCalls))
	}
	if dns.addCalls[0].Domain != "laptop.roam.example.com" {
		t.Errorf("domain = %q, want laptop.roam.example.com", dns.addCalls[0].Domain)
	}
	if dns.addCalls[0].IPAddress != "192.168.1.10" {
		t.Errorf("ip = %q, want 192.168.1.10 (highest priority member)", dns.addCalls[0].IPAddress)
	}

	var got db.Identity
	if err := gdb.First(&got, ident.ID).Error; err != nil {
		t.Fatalf("reloading identity: %v", err)
	}
	if !got.DNSRecordSynced || got.ActiveMAC != "aa:bb:cc:dd:ee:01" {
		t.Errorf("unexpected identity state: %+v", got)
	}
	if got.LastVerifiedAt == nil {
		t.Errorf("expected LastVerifiedAt to be set")
	}

	// Individual devices in the identity must not get their own records.
	var devices []db.Device
	if err := gdb.Find(&devices).Error; err != nil {
		t.Fatalf("querying devices: %v", err)
	}
	for _, d := range devices {
		if d.DNSRecordSynced {
			t.Errorf("expected identity member device %s to never sync its own record", d.MAC)
		}
	}
}

func TestRunOnceIdentityFailsOverWhenPreferredMemberDead(t *testing.T) {
	gdb := testDB(t)
	seedAppSettings(t, gdb)
	seedTechnitium(t, gdb)
	ctrl := seedController(t, gdb, "lan.example.com")

	now := time.Now()
	if err := gdb.Create(&db.Device{MAC: "aa:bb:cc:dd:ee:01", ControllerID: ctrl.ID, IPAddress: "192.168.1.10", LastSeen: now}).Error; err != nil {
		t.Fatalf("seeding device 1: %v", err)
	}
	if err := gdb.Create(&db.Device{MAC: "aa:bb:cc:dd:ee:02", ControllerID: ctrl.ID, IPAddress: "192.168.1.20", LastSeen: now}).Error; err != nil {
		t.Fatalf("seeding device 2: %v", err)
	}

	ident := db.Identity{Name: "laptop", Zone: "roam.example.com"}
	if err := gdb.Create(&ident).Error; err != nil {
		t.Fatalf("seeding identity: %v", err)
	}
	if err := gdb.Create(&db.IdentityMember{IdentityID: ident.ID, MAC: "aa:bb:cc:dd:ee:01", Priority: 0}).Error; err != nil {
		t.Fatalf("seeding member 1: %v", err)
	}
	if err := gdb.Create(&db.IdentityMember{IdentityID: ident.ID, MAC: "aa:bb:cc:dd:ee:02", Priority: 1}).Error; err != nil {
		t.Fatalf("seeding member 2: %v", err)
	}

	dns := &fakeDNS{}
	uc := fakeUnifi{}
	e := newTestEngine(gdb, dns, uc)
	e.NewIsAlive = func() func(ip string) bool {
		return func(ip string) bool { return ip == "192.168.1.20" }
	}
	e.VerifyDNS = func(ctx context.Context, dnsHost, fqdn, expectedIP string, timeout time.Duration) error { return nil }

	if err := e.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if len(dns.addCalls) != 1 || dns.addCalls[0].IPAddress != "192.168.1.20" {
		t.Fatalf("expected failover to the alive lower-priority member, got %+v", dns.addCalls)
	}
}

func TestRunOnceIdentityDeletesRecordWhenNoMemberIsFresh(t *testing.T) {
	gdb := testDB(t)
	seedAppSettings(t, gdb)
	seedTechnitium(t, gdb)
	ctrl := seedController(t, gdb, "lan.example.com")

	stale := time.Now().Add(-30 * 24 * time.Hour)
	if err := gdb.Create(&db.Device{MAC: "aa:bb:cc:dd:ee:01", ControllerID: ctrl.ID, IPAddress: "192.168.1.10", LastSeen: stale}).Error; err != nil {
		t.Fatalf("seeding device: %v", err)
	}

	ident := db.Identity{
		Name:            "laptop",
		Zone:            "roam.example.com",
		ActiveMAC:       "aa:bb:cc:dd:ee:01",
		IPAddress:       "192.168.1.10",
		SyncedHostname:  "laptop",
		DNSRecordSynced: true,
	}
	if err := gdb.Create(&ident).Error; err != nil {
		t.Fatalf("seeding identity: %v", err)
	}
	if err := gdb.Create(&db.IdentityMember{IdentityID: ident.ID, MAC: "aa:bb:cc:dd:ee:01", Priority: 0}).Error; err != nil {
		t.Fatalf("seeding member: %v", err)
	}

	dns := &fakeDNS{}
	uc := fakeUnifi{}
	e := newTestEngine(gdb, dns, uc)
	e.NewIsAlive = func() func(ip string) bool { return func(string) bool { return true } }
	e.VerifyDNS = func(ctx context.Context, dnsHost, fqdn, expectedIP string, timeout time.Duration) error { return nil }

	if err := e.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if len(dns.deleteCalls) != 1 {
		t.Fatalf("expected 1 DeleteRecord call, got %d", len(dns.deleteCalls))
	}

	var got db.Identity
	if err := gdb.First(&got, ident.ID).Error; err != nil {
		t.Fatalf("reloading identity: %v", err)
	}
	if got.DNSRecordSynced || got.ActiveMAC != "" {
		t.Errorf("expected identity record torn down, got %+v", got)
	}
}

func TestRunOnceNoIdentitiesSkipsIdentitySync(t *testing.T) {
	gdb := testDB(t)
	seedAppSettings(t, gdb)
	seedTechnitium(t, gdb)
	seedController(t, gdb, "lan.example.com")

	dns := &fakeDNS{}
	uc := fakeUnifi{}
	e := newTestEngine(gdb, dns, uc)

	if err := e.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
}
