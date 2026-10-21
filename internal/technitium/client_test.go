package technitium

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/Ohgwen/on-netreg/internal/config"
)

// fakeServer is a minimal Technitium API stand-in that holds A records in
// memory and, like the real server, rejects A-record deletes/updates that
// don't name the record's ipAddress.
type fakeServer struct {
	mu      sync.Mutex
	records map[string][]string // domain -> A record values
	calls   []url.Values        // every request's parameters, by path
	paths   []string
}

func newFakeServer(t *testing.T, records map[string][]string) (*fakeServer, *Client) {
	t.Helper()
	fs := &fakeServer{records: records}
	srv := httptest.NewServer(http.HandlerFunc(fs.handle))
	t.Cleanup(srv.Close)
	return fs, New(config.TechnitiumConfig{BaseURL: srv.URL, Username: "u", Password: "p"})
}

func (fs *fakeServer) handle(w http.ResponseWriter, r *http.Request) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	q := r.URL.Query()
	fs.calls = append(fs.calls, q)
	fs.paths = append(fs.paths, r.URL.Path)
	reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	missingIP := func() { reply(map[string]string{"status": "error", "errorMessage": "Parameter 'ipAddress' missing."}) }

	domain := q.Get("domain")
	switch r.URL.Path {
	case "/api/user/login":
		reply(map[string]string{"status": "ok", "token": "t"})
	case "/api/zones/records/get":
		var recs []map[string]any
		for _, ip := range fs.records[domain] {
			recs = append(recs, map[string]any{"name": domain, "type": "A", "rData": map[string]string{"ipAddress": ip}})
		}
		reply(map[string]any{"status": "ok", "response": map[string]any{"records": recs}})
	case "/api/zones/records/delete":
		ip := q.Get("ipAddress")
		if ip == "" {
			missingIP()
			return
		}
		fs.records[domain] = remove(fs.records[domain], ip)
		reply(map[string]string{"status": "ok"})
	case "/api/zones/records/update":
		ip := q.Get("ipAddress")
		if ip == "" {
			missingIP()
			return
		}
		fs.records[domain] = remove(fs.records[domain], ip)
		newDomain := q.Get("newDomain")
		if newDomain == "" {
			newDomain = domain
		}
		fs.records[newDomain] = append(fs.records[newDomain], q.Get("newIpAddress"))
		reply(map[string]string{"status": "ok"})
	case "/api/zones/records/add":
		if q.Get("overwrite") == "true" {
			fs.records[domain] = nil
		}
		fs.records[domain] = append(fs.records[domain], q.Get("ipAddress"))
		reply(map[string]string{"status": "ok"})
	default:
		http.NotFound(w, r)
	}
}

func remove(ips []string, ip string) []string {
	var out []string
	for _, v := range ips {
		if v != ip {
			out = append(out, v)
		}
	}
	return out
}

func TestDeleteRecordWithoutIPDeletesCurrentRecords(t *testing.T) {
	fs, c := newFakeServer(t, map[string][]string{"tv.iot.example": {"10.0.0.5", "10.0.0.6"}})

	err := c.DeleteRecord(context.Background(), DeleteRecordRequest{Domain: "tv.iot.example", Zone: "iot.example", Type: "A"})
	if err != nil {
		t.Fatalf("DeleteRecord: %v", err)
	}
	if got := fs.records["tv.iot.example"]; len(got) != 0 {
		t.Fatalf("expected all records deleted, got %v", got)
	}
}

func TestDeleteRecordWithoutIPWhenAlreadyGone(t *testing.T) {
	_, c := newFakeServer(t, map[string][]string{})

	err := c.DeleteRecord(context.Background(), DeleteRecordRequest{Domain: "tv.iot.example", Zone: "iot.example", Type: "A"})
	if err != nil {
		t.Fatalf("expected no error deleting an absent record, got %v", err)
	}
}

func TestUpdateRecordWithoutIPUsesCurrentValue(t *testing.T) {
	fs, c := newFakeServer(t, map[string][]string{"tv.iot.example": {"10.0.0.5"}})

	err := c.UpdateRecord(context.Background(), UpdateRecordRequest{
		Domain: "tv.iot.example", Zone: "iot.example", Type: "A",
		NewDomain: "h-tv.iot.example", NewIPAddress: "10.0.0.9",
	})
	if err != nil {
		t.Fatalf("UpdateRecord: %v", err)
	}
	if got := fs.records["h-tv.iot.example"]; len(got) != 1 || got[0] != "10.0.0.9" {
		t.Fatalf("expected renamed record at new IP, got %v", got)
	}
	if got := fs.records["tv.iot.example"]; len(got) != 0 {
		t.Fatalf("expected old record gone, got %v", got)
	}
}

func TestUpdateRecordWithoutIPAndNoRecordAdds(t *testing.T) {
	fs, c := newFakeServer(t, map[string][]string{})

	err := c.UpdateRecord(context.Background(), UpdateRecordRequest{
		Domain: "tv.iot.example", Zone: "iot.example", Type: "A", NewIPAddress: "10.0.0.9",
	})
	if err != nil {
		t.Fatalf("UpdateRecord: %v", err)
	}
	if got := fs.records["tv.iot.example"]; len(got) != 1 || got[0] != "10.0.0.9" {
		t.Fatalf("expected record added, got %v", got)
	}
}

// lastCall returns the parameters of the most recent request to path.
func (fs *fakeServer) lastCall(path string) url.Values {
	for i := len(fs.paths) - 1; i >= 0; i-- {
		if fs.paths[i] == path {
			return fs.calls[i]
		}
	}
	return nil
}

func TestUpdateRecordCarriesPTR(t *testing.T) {
	fs, c := newFakeServer(t, map[string][]string{"tv.iot.example": {"10.0.0.5"}})

	err := c.UpdateRecord(context.Background(), UpdateRecordRequest{
		Domain: "tv.iot.example", Zone: "iot.example", Type: "A", IPAddress: "10.0.0.5",
		NewDomain: "h-tv.iot.example", NewIPAddress: "10.0.0.5", PTR: true, CreatePTRZone: true,
	})
	if err != nil {
		t.Fatalf("UpdateRecord: %v", err)
	}
	q := fs.lastCall("/api/zones/records/update")
	if q.Get("ptr") != "true" || q.Get("createPtrZone") != "true" {
		t.Errorf("update params = %v, want ptr and createPtrZone set", q)
	}
}

func TestSetPTRWritesReverseRecord(t *testing.T) {
	fs, c := newFakeServer(t, map[string][]string{})

	if err := c.SetPTR(context.Background(), "10.200.55.0", "on-util-01.infra.example", 3600); err != nil {
		t.Fatalf("SetPTR: %v", err)
	}
	q := fs.lastCall("/api/zones/records/add")
	if q.Get("domain") != "0.55.200.10.in-addr.arpa" || q.Get("type") != "PTR" ||
		q.Get("ptrName") != "on-util-01.infra.example" || q.Get("overwrite") != "true" {
		t.Errorf("add params = %v", q)
	}
	// No zone: Technitium picks the closest authoritative reverse zone.
	if q.Has("zone") {
		t.Errorf("zone sent as %q, want it omitted", q.Get("zone"))
	}
}

func TestReverseName(t *testing.T) {
	for ip, want := range map[string]string{
		"10.200.55.0": "0.55.200.10.in-addr.arpa",
		"2001:db8::1": "1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2.ip6.arpa",
	} {
		got, err := ReverseName(ip)
		if err != nil || got != want {
			t.Errorf("ReverseName(%s) = %q, %v; want %q", ip, got, err, want)
		}
	}
	if _, err := ReverseName("nope"); err == nil {
		t.Error("expected an error for a non-IP")
	}
}
