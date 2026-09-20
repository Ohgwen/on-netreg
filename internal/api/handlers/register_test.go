package handlers

import (
	"context"
	"fmt"
	"github.com/Ohgwen/on-netreg/internal/api/web"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/Ohgwen/on-netreg/internal/config"
	"github.com/Ohgwen/on-netreg/internal/db"
	"github.com/Ohgwen/on-netreg/internal/directory"
)

type fakeDirectory struct{ users map[string]directory.User }

func (f fakeDirectory) Search(ctx context.Context, q string) ([]directory.User, error) {
	return nil, nil
}

func (f fakeDirectory) Lookup(ctx context.Context, username string) (directory.User, error) {
	if u, ok := f.users[username]; ok {
		return u, nil
	}
	return directory.User{}, directory.ErrNotFound
}

func testHandlers(t *testing.T) (*Handlers, *gorm.DB) {
	t.Helper()
	gdb, err := db.Open(config.DatabaseConfig{Driver: "sqlite", DSN: fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())})
	if err != nil {
		t.Fatal(err)
	}
	return &Handlers{
		DB:     gdb,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Directory: fakeDirectory{users: map[string]directory.User{
			"alice": {Username: "alice", Name: "Alice A", Email: "alice@example.com"},
		}},
	}, gdb
}

func TestRegisterNewDevice(t *testing.T) {
	h, gdb := testHandlers(t)
	dev, existed, err := h.registerDevice(context.Background(), "admin", "AA-BB-CC-DD-EE-01", "Front Printer", "alice")
	if err != nil || existed {
		t.Fatalf("registerDevice = %v, existed=%v", err, existed)
	}
	var got db.Device
	if err := gdb.First(&got, dev.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.MAC != "aa:bb:cc:dd:ee:01" || got.EffectiveHostname() != "front-printer" {
		t.Errorf("stored %q / %q", got.MAC, got.EffectiveHostname())
	}
	if got.ControllerID != 0 || !got.Registered || got.OwnerUsername != "alice" || got.OwnerName != "Alice A" {
		t.Errorf("unexpected device: %+v", got)
	}
}

func TestRegisterExistingDeviceChangesHostnameKeepsRecord(t *testing.T) {
	h, gdb := testHandlers(t)
	seen := db.Device{MAC: "aa:bb:cc:dd:ee:02", Hostname: "old", ControllerID: 3, DNSRecordSynced: true, Excluded: true, IPAddress: "10.0.0.5"}
	if err := gdb.Create(&seen).Error; err != nil {
		t.Fatal(err)
	}
	dev, existed, err := h.registerDevice(context.Background(), "admin", "aa:bb:cc:dd:ee:02", "new-name", "")
	if err != nil || !existed {
		t.Fatalf("registerDevice = %v, existed=%v", err, existed)
	}
	if dev.EffectiveHostname() != "new-name" || dev.Excluded || dev.ControllerID != 3 {
		t.Errorf("unexpected device: %+v", dev)
	}
	// Must stay synced so the next reconcile issues an in-place update
	// rather than a create that would orphan the old record.
	if !dev.DNSRecordSynced {
		t.Error("DNSRecordSynced was cleared")
	}
	var n int64
	gdb.Model(&db.Device{}).Count(&n)
	if n != 1 {
		t.Errorf("%d devices, want 1", n)
	}
}

func TestRegisterValidation(t *testing.T) {
	h, gdb := testHandlers(t)
	member := db.Device{MAC: "aa:bb:cc:dd:ee:03", Hostname: "m", IdentityID: new(uint)}
	*member.IdentityID = 1
	gdb.Create(&member)

	cases := []struct{ name, mac, host, owner string }{
		{"bad mac", "nope", "x", ""},
		{"empty hostname", "aa:bb:cc:dd:ee:04", "  !!  ", ""},
		{"unknown owner", "aa:bb:cc:dd:ee:04", "x", "mallory"},
		{"identity member", "aa:bb:cc:dd:ee:03", "x", ""},
	}
	for _, c := range cases {
		_, _, err := h.registerDevice(context.Background(), "admin", c.mac, c.host, c.owner)
		if _, ok := err.(inputError); !ok {
			t.Errorf("%s: err = %v, want inputError", c.name, err)
		}
	}
	var n int64
	gdb.Model(&db.Device{}).Where("mac = ?", "aa:bb:cc:dd:ee:04").Count(&n)
	if n != 0 {
		t.Error("invalid registration still created a device")
	}
}

func TestRegisterWithoutLDAPRejectsOwner(t *testing.T) {
	h, _ := testHandlers(t)
	h.Directory = nil
	if _, _, err := h.registerDevice(context.Background(), "admin", "aa:bb:cc:dd:ee:05", "x", "alice"); err == nil {
		t.Error("expected error assigning owner with LDAP disabled")
	}
}

func TestCreateAliasValidation(t *testing.T) {
	h, gdb := testHandlers(t)
	h.CurrentUser = func(*http.Request) string { return "admin" }
	a := db.Device{MAC: "aa:bb:cc:dd:ee:10", Hostname: "web1", Zone: "lan"}
	b := db.Device{MAC: "aa:bb:cc:dd:ee:11", Hostname: "web2", Zone: "lan"}
	gdb.Create(&a)
	gdb.Create(&b)
	r := httptest.NewRequest("POST", "/", nil)

	if _, err := h.createAlias(r, a, "WWW"); err != nil {
		t.Fatalf("valid alias rejected: %v", err)
	}
	for name, c := range map[string]struct {
		dev   db.Device
		label string
	}{
		"blank":              {a, " !! "},
		"own hostname":       {a, "web1"},
		"other's hostname":   {a, "web2"},
		"duplicate":          {a, "www"},
		"other device alias": {b, "www"},
	} {
		if _, err := h.createAlias(r, c.dev, c.label); err == nil {
			t.Errorf("%s: expected rejection", name)
		} else if _, ok := err.(inputError); !ok {
			t.Errorf("%s: err = %v, want inputError", name, err)
		}
	}
}

func TestDashboardShowsAndFiltersByConsole(t *testing.T) {
	h, gdb := testHandlers(t)
	pages, err := web.Templates()
	if err != nil {
		t.Fatal(err)
	}
	h.Pages = pages
	h.CurrentUser = func(*http.Request) string { return "admin" }
	h.IsAdmin = func(*http.Request) bool { return true }
	c1 := db.UnifiController{Name: "HQ-UDM", BaseURL: "x"}
	c2 := db.UnifiController{Name: "Branch-UDM", BaseURL: "x"}
	gdb.Create(&c1)
	gdb.Create(&c2)
	gdb.Create(&db.Device{MAC: "aa:bb:cc:dd:ee:20", Hostname: "on-hq", ControllerID: c1.ID})
	gdb.Create(&db.Device{MAC: "aa:bb:cc:dd:ee:21", Hostname: "on-branch", ControllerID: c2.ID})

	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, httptest.NewRequest("GET", fmt.Sprintf("/?console=%d", c2.ID), nil))
	body := rec.Body.String()
	if !strings.Contains(body, "on-branch") || strings.Contains(body, "on-hq") {
		t.Errorf("console filter did not narrow the list:\n%s", body)
	}
	if !strings.Contains(body, "Branch-UDM") {
		t.Error("console name not shown")
	}
}
