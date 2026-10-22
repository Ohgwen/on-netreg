package registry

import (
	"testing"
	"time"

	"github.com/Ohgwen/on-netreg/internal/db"
	"github.com/Ohgwen/on-netreg/internal/unifi"
)

func TestNetworkMatcher(t *testing.T) {
	networks := []db.UnifiNetwork{
		{ID: 1, ControllerID: 1, Name: "LAN", IPSubnet: "10.0.0.1/16"},
		{ID: 2, ControllerID: 1, Name: "IoT", IPSubnet: "10.0.20.1/24"},
		{ID: 3, ControllerID: 2, Name: "Branch IoT", IPSubnet: "10.0.20.1/24"},
		{ID: 4, ControllerID: 2, Name: "Branch LAN", IPSubnet: "10.1.0.1/24"},
		{ID: 5, Manual: true, Name: "Lab", IPSubnet: "10.1.0.0/24"},
		{ID: 6, ControllerID: 1, Name: "Broken", IPSubnet: "not-a-subnet"},
	}
	m := NewNetworkMatcher(networks)

	cases := []struct {
		ip     string
		ctrl   uint
		want   uint // 0 = no match
		reason string
	}{
		{"10.0.5.9", 1, 1, "only the /16 contains it"},
		{"10.0.20.9", 1, 2, "longest prefix wins over the /16"},
		{"10.0.20.9", 2, 3, "same subnet on two consoles: the device's own console wins"},
		{"10.1.0.9", 2, 5, "a manual network beats an imported one with the same subnet"},
		{"192.168.1.1", 1, 0, "no subnet contains it"},
		{"", 1, 0, "no address"},
	}
	for _, c := range cases {
		got, ok := m.Match(c.ip, c.ctrl)
		if c.want == 0 {
			if ok {
				t.Errorf("%s: matched %q, want none (%s)", c.ip, got.Name, c.reason)
			}
			continue
		}
		if !ok || got.ID != c.want {
			t.Errorf("%s via console %d: got %d (ok=%v), want %d (%s)", c.ip, c.ctrl, got.ID, ok, c.want, c.reason)
		}
	}
}

// A device that drops off (no address) keeps the network and zone it was
// last placed in instead of falling back to the default zone.
func TestReconcileKeepsNetworkWhileAddressless(t *testing.T) {
	netID := uint(7)
	existing := db.Device{MAC: "10:bb:cc:dd:ee:01", Hostname: "laptop", Zone: "iot.example.com", NetworkID: &netID, ControllerID: 1}
	client := makeClient(existing.MAC, "Laptop", "")
	client.IP = ""

	result := Reconcile(time.Now(), 1, []db.Device{existing}, []unifi.NetworkClient{client}, testDNSConfig, fixedZone(testZone), nil)
	dev := result.Devices[0]
	if dev.NetworkID == nil || *dev.NetworkID != netID || dev.Zone != "iot.example.com" {
		t.Errorf("network/zone = %v/%q, want %d/iot.example.com", dev.NetworkID, dev.Zone, netID)
	}
}

// The resolver must see a static device's registered address, since that is
// the address that decides its network.
func TestReconcileResolvesStaticDeviceByAssignedIP(t *testing.T) {
	existing := db.Device{MAC: "10:bb:cc:dd:ee:02", Hostname: "nas", AddressMode: db.AddressStatic, AssignedIP: "10.0.30.5", ControllerID: 1}
	client := makeClient(existing.MAC, "NAS", "")
	var resolvedIP string
	resolver := func(c unifi.NetworkClient) NetworkInfo {
		resolvedIP = c.IP
		return NetworkInfo{Zone: testZone}
	}
	Reconcile(time.Now(), 1, []db.Device{existing}, []unifi.NetworkClient{client}, testDNSConfig, resolver, nil)
	if resolvedIP != "10.0.30.5" {
		t.Errorf("resolver saw %q, want the static address 10.0.30.5", resolvedIP)
	}
}
