package sync

import (
	"context"
	"testing"

	"github.com/Ohgwen/on-netreg/internal/config"
	"github.com/Ohgwen/on-netreg/internal/db"
)

func TestSyncAliasesLifecycle(t *testing.T) {
	gdb := testDB(t)
	dns := &fakeDNS{}
	e := newTestEngine(gdb, dns, fakeUnifi{})
	ctx := context.Background()
	cfg := config.TechnitiumConfig{TTL: 300}

	dev := db.Device{MAC: "aa:bb:cc:dd:ee:01", Hostname: "web1", Zone: "lan.example.com", DNSRecordSynced: true, ControllerID: 1}
	gdb.Create(&dev)
	alias := db.DeviceAlias{DeviceID: dev.ID, Label: "www"}
	gdb.Create(&alias)

	// Device has an A record: CNAME is created.
	if err := e.syncAliases(ctx, dns, cfg); err != nil {
		t.Fatal(err)
	}
	if len(dns.addCalls) != 1 {
		t.Fatalf("add calls = %d, want 1", len(dns.addCalls))
	}
	add := dns.addCalls[0]
	if add.Type != "CNAME" || add.Domain != "www.lan.example.com" || add.CNAME != "web1.lan.example.com" || add.TTL != 300 {
		t.Errorf("unexpected add: %+v", add)
	}

	// Steady state: nothing more to do.
	e.syncAliases(ctx, dns, cfg)
	if len(dns.addCalls) != 1 || len(dns.deleteCalls) != 0 {
		t.Fatalf("not idempotent: adds=%d deletes=%d", len(dns.addCalls), len(dns.deleteCalls))
	}

	// Device renamed: old CNAME removed, new one points at the new name.
	gdb.Model(&dev).Update("hostname", "web2")
	e.syncAliases(ctx, dns, cfg)
	if len(dns.deleteCalls) != 1 || dns.deleteCalls[0].Domain != "www.lan.example.com" || dns.deleteCalls[0].Type != "CNAME" {
		t.Errorf("delete calls = %+v", dns.deleteCalls)
	}
	if len(dns.addCalls) != 2 || dns.addCalls[1].CNAME != "web2.lan.example.com" {
		t.Errorf("add calls = %+v", dns.addCalls)
	}

	// Device loses its A record: CNAME comes down, alias row stays.
	gdb.Model(&dev).Update("dns_record_synced", false)
	e.syncAliases(ctx, dns, cfg)
	if len(dns.deleteCalls) != 2 {
		t.Errorf("delete calls = %d, want 2", len(dns.deleteCalls))
	}
	var got db.DeviceAlias
	gdb.First(&got, alias.ID)
	if got.Synced {
		t.Error("alias still marked synced")
	}

	// Device forgotten: alias row is cleaned up.
	gdb.Delete(&dev)
	e.syncAliases(ctx, dns, cfg)
	var n int64
	gdb.Model(&db.DeviceAlias{}).Count(&n)
	if n != 0 {
		t.Errorf("%d aliases remain for a forgotten device", n)
	}
}

func TestSyncAliasesKeepsStateWhenDeleteFails(t *testing.T) {
	gdb := testDB(t)
	dns := &fakeDNS{}
	e := newTestEngine(gdb, dns, fakeUnifi{})
	cfg := config.TechnitiumConfig{}

	dev := db.Device{MAC: "aa:bb:cc:dd:ee:02", Hostname: "h", Zone: "z.example", DNSRecordSynced: true}
	gdb.Create(&dev)
	gdb.Create(&db.DeviceAlias{DeviceID: dev.ID, Label: "a"})
	e.syncAliases(context.Background(), dns, cfg)

	gdb.Model(&dev).Update("dns_record_synced", false)
	dns.deleteErr = context.DeadlineExceeded
	e.syncAliases(context.Background(), dns, cfg)

	var got db.DeviceAlias
	gdb.First(&got)
	if !got.Synced || got.LastSyncError == "" {
		t.Errorf("failed delete must leave alias synced with an error: %+v", got)
	}
}

func TestSyncAliasesUsesPinnedZone(t *testing.T) {
	gdb := testDB(t)
	dns := &fakeDNS{}
	e := newTestEngine(gdb, dns, fakeUnifi{})

	dev := db.Device{MAC: "aa:bb:cc:dd:ee:03", Hostname: "web1", Zone: "lan.example.com", DNSRecordSynced: true}
	gdb.Create(&dev)
	gdb.Create(&db.DeviceAlias{DeviceID: dev.ID, Label: "shop", Zone: "example.com"})

	e.syncAliases(context.Background(), dns, config.TechnitiumConfig{})
	if len(dns.addCalls) != 1 {
		t.Fatalf("adds = %d", len(dns.addCalls))
	}
	add := dns.addCalls[0]
	if add.Domain != "shop.example.com" || add.Zone != "example.com" || add.CNAME != "web1.lan.example.com" {
		t.Errorf("unexpected add: %+v", add)
	}

	// Removal targets the alias's own zone, not the device's.
	gdb.Model(&dev).Update("dns_record_synced", false)
	e.syncAliases(context.Background(), dns, config.TechnitiumConfig{})
	if len(dns.deleteCalls) != 1 || dns.deleteCalls[0].Zone != "example.com" || dns.deleteCalls[0].Domain != "shop.example.com" {
		t.Errorf("delete calls = %+v", dns.deleteCalls)
	}
}
