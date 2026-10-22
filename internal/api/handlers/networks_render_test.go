package handlers

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Ohgwen/on-netreg/internal/api/web"
	"github.com/Ohgwen/on-netreg/internal/db"
	"github.com/Ohgwen/on-netreg/internal/settings"
	"github.com/Ohgwen/on-netreg/internal/technitium"
)

// fakeTechnitium serves just enough of the Technitium API for the network
// and device pages: one forward zone and one reverse zone.
func fakeTechnitium(t *testing.T) *httptest.Server {
	t.Helper()
	rec := func(name, typ string, rdata map[string]any) map[string]any {
		return map[string]any{"name": name, "type": typ, "ttl": 3600, "rData": rdata, "disabled": false,
			"comments": "", "lastUsedOn": "0001-01-01T00:00:00", "lastModified": "2026-10-01T12:00:00Z"}
	}
	forward := []map[string]any{
		rec("iot.example.com", "SOA", map[string]any{"primaryNameServer": "ns", "responsiblePerson": "hostmaster", "serial": 1}),
		rec("cam.iot.example.com", "A", map[string]any{"ipAddress": "10.0.20.5"}),
		rec("stray.iot.example.com", "A", map[string]any{"ipAddress": "10.0.20.9"}),
		rec("other.iot.example.com", "A", map[string]any{"ipAddress": "10.9.9.9"}),
		rec("www.iot.example.com", "CNAME", map[string]any{"cname": "stray.iot.example.com"}),
	}
	reverse := []map[string]any{
		rec("5.20.0.10.in-addr.arpa", "PTR", map[string]any{"ptrName": "cam.iot.example.com"}),
		rec("5.99.0.10.in-addr.arpa", "PTR", map[string]any{"ptrName": "elsewhere.example.com"}),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply := map[string]any{"status": "ok"}
		switch r.URL.Path {
		case "/api/user/login":
			reply["token"] = "tok"
		case "/api/zones/list":
			reply["response"] = map[string]any{"zones": []map[string]any{{"name": "iot.example.com", "type": "Primary"}}}
		case "/api/zones/records/get":
			domain := r.URL.Query().Get("domain")
			var out []map[string]any
			switch {
			case strings.HasSuffix(domain, "in-addr.arpa"):
				for _, rr := range reverse {
					if r.URL.Query().Get("listZone") == "true" || rr["name"] == domain {
						out = append(out, rr)
					}
				}
			default:
				for _, rr := range forward {
					if r.URL.Query().Get("listZone") == "true" || rr["name"] == domain {
						out = append(out, rr)
					}
				}
			}
			reply["response"] = map[string]any{"records": out}
		}
		json.NewEncoder(w).Encode(reply)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestNetworkAndDevicePagesRender(t *testing.T) {
	h, gdb := testHandlers(t)
	pages, err := web.Templates()
	if err != nil {
		t.Fatal(err)
	}
	srv := fakeTechnitium(t)
	key := settings.Key("test")
	if err := settings.SaveTechnitium(gdb, key, &db.TechnitiumSettings{BaseURL: srv.URL, Username: "admin"}, "pw"); err != nil {
		t.Fatal(err)
	}
	h.Pages = pages
	h.CurrentUser = func(*http.Request) string { return "admin" }
	h.IsAdmin = func(*http.Request) bool { return true }
	h.DNS = func(ctx context.Context) (DNSClient, error) {
		cfg, err := settings.LoadTechnitium(gdb, key)
		if err != nil {
			return nil, err
		}
		return technitium.New(cfg), nil
	}

	iot := db.UnifiNetwork{UnifiNetworkID: "manual:x", Manual: true, Name: "IoT", IPSubnet: "10.0.20.0/24", Zone: "iot.example.com"}
	gdb.Create(&iot)
	gdb.Create(&db.Device{MAC: "aa:bb:cc:dd:ee:01", Hostname: "cam", Zone: "iot.example.com", IPAddress: "10.0.20.5",
		NetworkID: &iot.ID, UniFiNetworkName: "LAN", DNSRecordSynced: true, FirstSeen: time.Now(), LastSeen: time.Now()})

	sh := &SettingsHandlers{DB: gdb, SecretKey: key, Pages: pages, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		CurrentUser: h.CurrentUser, IsAdmin: h.IsAdmin}

	get := func(handler http.Handler, path string) string {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 {
			t.Fatalf("GET %s = %d\n%s", path, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	list := get(sh.Routes(), "/settings/networks")
	for _, want := range []string{"IoT", "10.0.20.0/24", "Add a network"} {
		if !strings.Contains(list, want) {
			t.Errorf("networks page missing %q", want)
		}
	}

	detail := get(sh.Routes(), "/settings/networks/1")
	for _, want := range []string{"stray.iot.example.com", "www.iot.example.com", "device cam", "5.20.0.10.in-addr.arpa"} {
		if !strings.Contains(detail, want) {
			t.Errorf("network page missing %q", want)
		}
	}
	for _, unwanted := range []string{"other.iot.example.com", "5.99.0.10.in-addr.arpa"} {
		if strings.Contains(detail, unwanted) {
			t.Errorf("network page shows %q, which is outside the subnet", unwanted)
		}
	}
	if whole := get(sh.Routes(), "/settings/networks/1?all=1"); !strings.Contains(whole, "other.iot.example.com") {
		t.Error("whole-zone view should list records outside the subnet")
	}

	device := get(h.Routes(), "/devices/1")
	for _, want := range []string{"DNS records", "cam.iot.example.com", "10.0.20.5", "5.20.0.10.in-addr.arpa", "UniFi reports LAN"} {
		if !strings.Contains(device, want) {
			t.Errorf("device page missing %q", want)
		}
	}
}

// A record netreg publishes can't be deleted from the network page: the
// sync engine would only put it back.
func TestDeleteManagedRecordRefused(t *testing.T) {
	h, gdb := testHandlers(t)
	iot := db.UnifiNetwork{UnifiNetworkID: "manual:x", Manual: true, Name: "IoT", IPSubnet: "10.0.20.0/24"}
	gdb.Create(&iot)
	gdb.Create(&db.Device{MAC: "aa:bb:cc:dd:ee:01", Hostname: "cam", Zone: "iot.example.com", IPAddress: "10.0.20.5", DNSRecordSynced: true})
	sh := &SettingsHandlers{DB: gdb, Logger: h.Logger, CurrentUser: func(*http.Request) string { return "admin" }}

	body := "name=cam.iot.example.com&type=A&value=10.0.20.5&zone=iot.example.com"
	r := httptest.NewRequest("POST", "/settings/networks/1/records/delete", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	sh.Routes().ServeHTTP(rec, r)
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "published+by+device") {
		t.Errorf("redirect = %q, want a published-by refusal", loc)
	}
}
