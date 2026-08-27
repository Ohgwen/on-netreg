package registry

import (
	"testing"
	"time"

	"github.com/Ohgwen/on-netreg/internal/config"
	"github.com/Ohgwen/on-netreg/internal/db"
	"github.com/Ohgwen/on-netreg/internal/unifi"
)

var testDNSConfig = config.DNSConfig{FallbackPattern: "{vendor}-{macsuffix}"}

const testZone = "lan.example.com"

// fixedZone returns a ZoneResolver that always resolves to the given zone,
// with no network mapping and no known lease time.
func fixedZone(zone string) ZoneResolver {
	return func(unifi.NetworkClient) NetworkInfo {
		return NetworkInfo{Zone: zone}
	}
}

func TestReconcileCreatesNewDevice(t *testing.T) {
	now := time.Now()
	client := makeClient("aa:bb:cc:dd:ee:01", "Laptop", "")

	result := Reconcile(now, 1, nil, []unifi.NetworkClient{client}, testDNSConfig, fixedZone(testZone))

	if len(result.Devices) != 1 {
		t.Fatalf("expected 1 device, got %d", len(result.Devices))
	}
	if result.Devices[0].Hostname != "laptop" {
		t.Errorf("hostname = %q, want laptop", result.Devices[0].Hostname)
	}
	if result.Devices[0].ControllerID != 1 {
		t.Errorf("controllerID = %d, want 1", result.Devices[0].ControllerID)
	}
	if result.Devices[0].Zone != testZone {
		t.Errorf("zone = %q, want %q", result.Devices[0].Zone, testZone)
	}
	if len(result.Changes) != 1 || result.Changes[0].Kind != ChangeCreate {
		t.Fatalf("expected 1 create change, got %+v", result.Changes)
	}
	if result.Changes[0].Zone != testZone {
		t.Errorf("change zone = %q, want %q", result.Changes[0].Zone, testZone)
	}
}

func TestReconcileNoChangeWhenNothingChanged(t *testing.T) {
	now := time.Now()
	existing := []db.Device{{
		MAC:             "aa:bb:cc:dd:ee:01",
		Hostname:        "laptop",
		Zone:            testZone,
		IPAddress:       "192.168.1.100",
		DNSRecordSynced: true,
		LastSeen:        now.Add(-time.Minute),
	}}
	client := makeClient("aa:bb:cc:dd:ee:01", "Laptop", "")

	result := Reconcile(now, 1, existing, []unifi.NetworkClient{client}, testDNSConfig, fixedZone(testZone))

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
		Zone:            testZone,
		IPAddress:       "192.168.1.50",
		DNSRecordSynced: true,
		LastSeen:        now.Add(-time.Minute),
	}}
	client := makeClient("aa:bb:cc:dd:ee:01", "Laptop", "")

	result := Reconcile(now, 1, existing, []unifi.NetworkClient{client}, testDNSConfig, fixedZone(testZone))

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

func TestReconcileZoneChangeEmitsDeleteAndCreate(t *testing.T) {
	now := time.Now()
	existing := []db.Device{{
		MAC:             "aa:bb:cc:dd:ee:01",
		Hostname:        "laptop",
		Zone:            "old.example.com",
		IPAddress:       "192.168.1.100",
		DNSRecordSynced: true,
		LastSeen:        now.Add(-time.Minute),
	}}
	client := makeClient("aa:bb:cc:dd:ee:01", "Laptop", "")

	result := Reconcile(now, 1, existing, []unifi.NetworkClient{client}, testDNSConfig, fixedZone("new.example.com"))

	if len(result.Changes) != 2 {
		t.Fatalf("expected 2 changes (delete+create), got %+v", result.Changes)
	}
	if result.Changes[0].Kind != ChangeDelete || result.Changes[0].PreviousZone != "old.example.com" {
		t.Errorf("expected delete from old zone, got %+v", result.Changes[0])
	}
	if result.Changes[1].Kind != ChangeCreate || result.Changes[1].Zone != "new.example.com" {
		t.Errorf("expected create in new zone, got %+v", result.Changes[1])
	}
}

func TestReconcileExcludedDeviceNeverSynced(t *testing.T) {
	now := time.Now()
	existing := []db.Device{{
		MAC:      "aa:bb:cc:dd:ee:01",
		Hostname: "laptop",
		Zone:     testZone,
		Excluded: true,
		LastSeen: now.Add(-time.Minute),
	}}
	client := makeClient("aa:bb:cc:dd:ee:01", "Laptop", "")

	result := Reconcile(now, 1, existing, []unifi.NetworkClient{client}, testDNSConfig, fixedZone(testZone))

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
		Zone:             testZone,
		OverrideHostname: &override,
		IPAddress:        "192.168.1.100",
		DNSRecordSynced:  true,
		LastSeen:         now.Add(-time.Minute),
	}}
	client := makeClient("aa:bb:cc:dd:ee:01", "Laptop", "")

	result := Reconcile(now, 1, existing, []unifi.NetworkClient{client}, testDNSConfig, fixedZone(testZone))

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

	result := Reconcile(now, 1, nil, clients, testDNSConfig, fixedZone(testZone))

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

func TestReconcileSameNameDifferentZonesNotDisambiguated(t *testing.T) {
	now := time.Now()
	clients := []unifi.NetworkClient{
		makeClient("aa:bb:cc:dd:ee:01", "Laptop", ""),
		makeClient("aa:bb:cc:dd:ee:02", "Laptop", ""),
	}
	zones := map[string]string{
		"aa:bb:cc:dd:ee:01": "lan.example.com",
		"aa:bb:cc:dd:ee:02": "iot.example.com",
	}
	resolver := func(c unifi.NetworkClient) NetworkInfo {
		return NetworkInfo{Zone: zones[c.MAC]}
	}

	result := Reconcile(now, 1, nil, clients, testDNSConfig, resolver)

	for _, d := range result.Devices {
		if d.Hostname != "laptop" {
			t.Errorf("expected hostname 'laptop' unchanged across zones, got %q for %s", d.Hostname, d.MAC)
		}
	}
}

func TestReconcileDoesNotRemoveByDefaultWhenAbsent(t *testing.T) {
	now := time.Now()
	existing := []db.Device{{
		MAC:             "aa:bb:cc:dd:ee:01",
		Hostname:        "laptop",
		Zone:            testZone,
		DNSRecordSynced: true,
		LastSeen:        now.Add(-30 * 24 * time.Hour),
	}}

	result := Reconcile(now, 1, existing, nil, testDNSConfig, fixedZone(testZone))

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
		Zone:            testZone,
		DNSRecordSynced: true,
		LastSeen:        now.Add(-10 * 24 * time.Hour),
	}}

	result := Reconcile(now, 1, existing, nil, cfg, fixedZone(testZone))

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
		Zone:            testZone,
		DNSRecordSynced: true,
		LastSeen:        now.Add(-3 * 24 * time.Hour),
	}}

	result := Reconcile(now, 1, existing, nil, cfg, fixedZone(testZone))

	if len(result.Changes) != 0 {
		t.Fatalf("expected no removal before threshold, got %+v", result.Changes)
	}
}

func TestReconcileEstimatesLeaseExpiryForDHCPClient(t *testing.T) {
	now := time.Now()
	client := makeClient("aa:bb:cc:dd:ee:01", "Laptop", "")
	resolver := func(unifi.NetworkClient) NetworkInfo {
		return NetworkInfo{Zone: testZone, LeaseSeconds: 3600}
	}

	result := Reconcile(now, 1, nil, []unifi.NetworkClient{client}, testDNSConfig, resolver)

	dev := result.Devices[0]
	if dev.LeaseEstimatedExpiry == nil {
		t.Fatalf("expected an estimated lease expiry")
	}
	want := now.Add(time.Hour)
	if !dev.LeaseEstimatedExpiry.Equal(want) {
		t.Errorf("lease expiry = %v, want %v", dev.LeaseEstimatedExpiry, want)
	}
}

func TestReconcileNoLeaseExpiryForFixedIPClient(t *testing.T) {
	now := time.Now()
	client := makeClient("aa:bb:cc:dd:ee:01", "Laptop", "")
	client.IsFixedIP = true
	resolver := func(unifi.NetworkClient) NetworkInfo {
		return NetworkInfo{Zone: testZone, LeaseSeconds: 3600}
	}

	result := Reconcile(now, 1, nil, []unifi.NetworkClient{client}, testDNSConfig, resolver)

	if result.Devices[0].LeaseEstimatedExpiry != nil {
		t.Errorf("expected no estimated lease expiry for a fixed-IP client")
	}
}
