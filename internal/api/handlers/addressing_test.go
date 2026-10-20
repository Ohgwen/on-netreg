package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Ohgwen/on-netreg/internal/db"
)

type fakeUnifiWriter struct {
	fixed   map[string]string // mac -> reserved IP
	cleared []string
	names   map[string]string
}

func (f *fakeUnifiWriter) SetFixedIP(ctx context.Context, mac, ip, networkID string) error {
	f.fixed[mac] = ip
	return nil
}

func (f *fakeUnifiWriter) ClearFixedIP(ctx context.Context, mac string) error {
	f.cleared = append(f.cleared, mac)
	delete(f.fixed, mac)
	return nil
}

func (f *fakeUnifiWriter) SetName(ctx context.Context, mac, name string) error {
	f.names[mac] = name
	return nil
}

func withUnifi(h *Handlers) *fakeUnifiWriter {
	f := &fakeUnifiWriter{fixed: map[string]string{}, names: map[string]string{}}
	h.Unifi = func(ctx context.Context, controllerID uint) (UnifiWriter, error) { return f, nil }
	h.CurrentUser = func(*http.Request) string { return "admin" }
	return f
}

// seenDevice creates a device a controller has already seen, on a mapped
// network with subnet 10.0.0.0/24.
func seenDevice(t *testing.T, h *Handlers, mac string) db.Device {
	t.Helper()
	network := db.UnifiNetwork{ControllerID: 1, UnifiNetworkID: "net1", Name: "IoT", IPSubnet: "10.0.0.1/24"}
	if err := h.DB.Create(&network).Error; err != nil {
		t.Fatal(err)
	}
	dev := db.Device{MAC: mac, Hostname: "tv", ControllerID: 1, NetworkID: &network.ID, IPAddress: "10.0.0.20"}
	if err := h.DB.Create(&dev).Error; err != nil {
		t.Fatal(err)
	}
	return dev
}

func TestChangeAddressingReservesInUnifi(t *testing.T) {
	h, gdb := testHandlers(t)
	f := withUnifi(h)
	dev := seenDevice(t, h, "aa:bb:cc:dd:ee:10")

	_, _, err := h.changeAddressing(context.Background(), httptest.NewRequest("POST", "/", nil), dev, db.AddressReserved, "10.0.0.50")
	if err != nil {
		t.Fatalf("changeAddressing: %v", err)
	}
	if f.fixed[dev.MAC] != "10.0.0.50" {
		t.Errorf("unifi reservation = %q, want 10.0.0.50", f.fixed[dev.MAC])
	}
	var got db.Device
	gdb.First(&got, dev.ID)
	if got.AddressMode != db.AddressReserved || got.AssignedIP != "10.0.0.50" || !got.IsFixedIP {
		t.Errorf("stored %+v", got)
	}

	// Back to DHCP clears the reservation.
	if _, _, err := h.changeAddressing(context.Background(), httptest.NewRequest("POST", "/", nil), got, db.AddressDHCP, ""); err != nil {
		t.Fatalf("changeAddressing: %v", err)
	}
	if len(f.cleared) != 1 {
		t.Errorf("expected reservation cleared, got %v", f.cleared)
	}
}

func TestChangeAddressingRejectsBadIPs(t *testing.T) {
	h, gdb := testHandlers(t)
	f := withUnifi(h)
	dev := seenDevice(t, h, "aa:bb:cc:dd:ee:11")
	other := db.Device{MAC: "aa:bb:cc:dd:ee:12", Hostname: "cam", IPAddress: "10.0.0.30"}
	gdb.Create(&other)

	for ip, want := range map[string]string{
		"not-an-ip": "not a valid IPv4",
		"10.9.9.9":  "outside",
		"10.0.0.30": "already in use",
	} {
		_, _, err := h.changeAddressing(context.Background(), httptest.NewRequest("POST", "/", nil), dev, db.AddressReserved, ip)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ip %s: err = %v, want %q", ip, err, want)
		}
	}
	if len(f.fixed) != 0 {
		t.Errorf("unifi written despite validation failure: %v", f.fixed)
	}
}

func TestRegisterRejectsReservationForUnseenDevice(t *testing.T) {
	h, _ := testHandlers(t)
	withUnifi(h)
	_, _, err := h.registerDevice(context.Background(), "admin", registerInput{MAC: "aa:bb:cc:dd:ee:13", Hostname: "printer", Mode: "reserved", IP: "10.0.0.60"})
	if err == nil || !strings.Contains(err.Error(), "connect once") {
		t.Fatalf("err = %v, want a must-connect-first error", err)
	}
}

func TestRegisterAllowsStaticForUnseenDevice(t *testing.T) {
	h, _ := testHandlers(t)
	withUnifi(h)
	in := registerInput{MAC: "aa:bb:cc:dd:ee:14", Hostname: "printer", Mode: "static", IP: "10.0.0.61"}
	dev, _, err := h.registerDevice(context.Background(), "admin", in)
	if err != nil {
		t.Fatalf("registerDevice: %v", err)
	}
	if !addressingChanged(dev, db.AddressStatic, in.IP) {
		t.Fatal("expected addressing to need applying after registration")
	}
	dev, _, err = h.changeAddressing(context.Background(), httptest.NewRequest("POST", "/", nil), dev, db.AddressStatic, in.IP)
	if err != nil || dev.AddressMode != db.AddressStatic || dev.AssignedIP != "10.0.0.61" {
		t.Fatalf("changeAddressing = %+v, %v", dev, err)
	}
}

func TestPushNameSetsUnifiAlias(t *testing.T) {
	h, gdb := testHandlers(t)
	f := withUnifi(h)
	dev := seenDevice(t, h, "aa:bb:cc:dd:ee:15")
	override := "h-fireplace-tv"
	dev.OverrideHostname = &override
	gdb.Save(&dev)

	req := httptest.NewRequest("POST", "/devices/1/push-name", nil)
	req.SetPathValue("id", "1")
	h.pushName(httptest.NewRecorder(), req)

	if f.names[dev.MAC] != "h-fireplace-tv" {
		t.Errorf("unifi name = %q, want h-fireplace-tv", f.names[dev.MAC])
	}
	var got db.Device
	gdb.First(&got, dev.ID)
	if got.UniFiName != "h-fireplace-tv" {
		t.Errorf("stored UniFi name %q", got.UniFiName)
	}
}
