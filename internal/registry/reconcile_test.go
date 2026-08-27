package registry

import (
	"testing"
	"time"

	"github.com/Ohgwen/on-netreg/internal/config"
	"github.com/Ohgwen/on-netreg/internal/db"
	"github.com/Ohgwen/on-netreg/internal/unifi"
)

var testDNSConfig = config.DNSConfig{FallbackPattern: "{vendor}-{macsuffix}"}

func TestReconcileCreatesNewDevice(t *testing.T) {
	now := time.Now()
	client := makeClient("aa:bb:cc:dd:ee:01", "Laptop", "")

	result := Reconcile(now, nil, []unifi.NetworkClient{client}, testDNSConfig)

	if len(result.Devices) != 1 {
		t.Fatalf("expected 1 device, got %d", len(result.Devices))
	}
	if result.Devices[0].Hostname != "laptop" {
		t.Errorf("hostname = %q, want laptop", result.Devices[0].Hostname)
	}
	if len(result.Changes) != 1 || result.Changes[0].Kind != ChangeCreate {
		t.Fatalf("expected 1 create change, got %+v", result.Changes)
	}
}

func TestReconcileNoChangeWhenNothingChanged(t *testing.T) {
	now := time.Now()
	existing := []db.Device{{
		MAC:             "aa:bb:cc:dd:ee:01",
		Hostname:        "laptop",
		IPAddress:       "192.168.1.100",
		DNSRecordSynced: true,
		LastSeen:        now.Add(-time.Minute),
	}}
	client := makeClient("aa:bb:cc:dd:ee:01", "Laptop", "")

	result := Reconcile(now, existing, []unifi.NetworkClient{client}, testDNSConfig)

	if len(result.Changes) != 0 {
		t.Fatalf("expected no changes, got %+v", result.Changes)
	}
	if !result.Devices[0].LastSeen.Equal(now) {
		t.Errorf("expected LastSeen updated to now")
	}
}

func TestReconcileUpdatesOnIPChange(t *testing.T) {
	now := time.Now()
	existing := []db.Device{{
		MAC:             "aa:bb:cc:dd:ee:01",
		Hostname:        "laptop",
		IPAddress:       "192.168.1.50",
		DNSRecordSynced: true,
		LastSeen:        now.Add(-time.Minute),
	}}
	client := makeClient("aa:bb:cc:dd:ee:01", "Laptop", "")

	result := Reconcile(now, existing, []unifi.NetworkClient{client}, testDNSConfig)

	if len(result.Changes) != 1 || result.Changes[0].Kind != ChangeUpdate {
		t.Fatalf("expected 1 update change, got %+v", result.Changes)
	}
	if result.Changes[0].IPAddress != "192.168.1.100" {
		t.Errorf("new IP = %q, want 192.168.1.100", result.Changes[0].IPAddress)
	}
	if result.Changes[0].PreviousIPAddress != "192.168.1.50" {
		t.Errorf("previous IP = %q, want 192.168.1.50", result.Changes[0].PreviousIPAddress)
	}
}

func TestReconcileExcludedDeviceNeverSynced(t *testing.T) {
	now := time.Now()
	existing := []db.Device{{
		MAC:      "aa:bb:cc:dd:ee:01",
		Hostname: "laptop",
		Excluded: true,
		LastSeen: now.Add(-time.Minute),
	}}
	client := makeClient("aa:bb:cc:dd:ee:01", "Laptop", "")

	result := Reconcile(now, existing, []unifi.NetworkClient{client}, testDNSConfig)

	if len(result.Changes) != 0 {
		t.Fatalf("expected no changes for excluded device, got %+v", result.Changes)
	}
	if !result.Devices[0].Excluded {
		t.Errorf("expected device to remain excluded")
	}
}

func TestReconcileOverrideHostnameWins(t *testing.T) {
	now := time.Now()
	override := "my-custom-name"
	existing := []db.Device{{
		MAC:              "aa:bb:cc:dd:ee:01",
		Hostname:         "laptop",
		OverrideHostname: &override,
		IPAddress:        "192.168.1.100",
		DNSRecordSynced:  true,
		LastSeen:         now.Add(-time.Minute),
	}}
	client := makeClient("aa:bb:cc:dd:ee:01", "Laptop", "")

	result := Reconcile(now, existing, []unifi.NetworkClient{client}, testDNSConfig)

	if len(result.Changes) != 1 || result.Changes[0].Hostname != "my-custom-name" {
		t.Fatalf("expected update to override hostname, got %+v", result.Changes)
	}
}

func TestReconcileCollisionGetsDisambiguated(t *testing.T) {
	now := time.Now()
	clients := []unifi.NetworkClient{
		makeClient("aa:bb:cc:dd:ee:01", "Laptop", ""),
		makeClient("aa:bb:cc:dd:ee:02", "Laptop", ""),
	}

	result := Reconcile(now, nil, clients, testDNSConfig)

	names := map[string]bool{}
	for _, d := range result.Devices {
		names[d.Hostname] = true
	}
	if len(names) != 2 {
		t.Fatalf("expected 2 distinct hostnames, got %v", names)
	}
	if !names["laptop"] {
		t.Errorf("expected one device to keep the base name 'laptop', got %v", names)
	}
}

func TestReconcileDoesNotRemoveByDefaultWhenAbsent(t *testing.T) {
	now := time.Now()
	existing := []db.Device{{
		MAC:             "aa:bb:cc:dd:ee:01",
		Hostname:        "laptop",
		DNSRecordSynced: true,
		LastSeen:        now.Add(-30 * 24 * time.Hour),
	}}

	result := Reconcile(now, existing, nil, testDNSConfig)

	if len(result.Changes) != 0 {
		t.Fatalf("expected no removal by default (remove_after_absence_days=0), got %+v", result.Changes)
	}
}

func TestReconcileRemovesAfterConfiguredAbsence(t *testing.T) {
	now := time.Now()
	cfg := config.DNSConfig{FallbackPattern: "{vendor}-{macsuffix}", RemoveAfterAbsenceDays: 7}
	existing := []db.Device{{
		MAC:             "aa:bb:cc:dd:ee:01",
		Hostname:        "laptop",
		DNSRecordSynced: true,
		LastSeen:        now.Add(-10 * 24 * time.Hour),
	}}

	result := Reconcile(now, existing, nil, cfg)

	if len(result.Changes) != 1 || result.Changes[0].Kind != ChangeDelete {
		t.Fatalf("expected 1 delete change, got %+v", result.Changes)
	}
}

func TestReconcileDoesNotRemoveBeforeAbsenceThreshold(t *testing.T) {
	now := time.Now()
	cfg := config.DNSConfig{FallbackPattern: "{vendor}-{macsuffix}", RemoveAfterAbsenceDays: 7}
	existing := []db.Device{{
		MAC:             "aa:bb:cc:dd:ee:01",
		Hostname:        "laptop",
		DNSRecordSynced: true,
		LastSeen:        now.Add(-3 * 24 * time.Hour),
	}}

	result := Reconcile(now, existing, nil, cfg)

	if len(result.Changes) != 0 {
		t.Fatalf("expected no removal before threshold, got %+v", result.Changes)
	}
}
