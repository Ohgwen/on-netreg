package handlers

import (
	"net"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Ohgwen/on-netreg/internal/db"
)

func TestBuildRecordRequest(t *testing.T) {
	n := db.UnifiNetwork{Name: "IoT", IPSubnet: "10.0.20.1/24"}
	form := func(kv ...string) *url.Values {
		v := url.Values{}
		for i := 0; i < len(kv); i += 2 {
			v.Set(kv[i], kv[i+1])
		}
		return &v
	}
	cases := []struct {
		form    *url.Values
		domain  string
		wantErr string
	}{
		{form("type", "A", "name", "cam", "value", "10.0.20.5", "ptr", "on"), "cam.iot.example.com", ""},
		{form("type", "A", "name", "cam.iot.example.com.", "value", "10.0.20.5"), "cam.iot.example.com", ""},
		{form("type", "A", "name", "@", "value", "10.0.20.5"), "iot.example.com", ""},
		{form("type", "A", "name", "cam", "value", "10.0.99.5"), "", "outside"},
		{form("type", "CNAME", "name", "www", "value", "cam.iot.example.com."), "www.iot.example.com", ""},
		{form("type", "CNAME", "name", "@", "value", "cam"), "", "apex"},
		{form("type", "PTR", "name", "10.0.20.5", "value", "cam.iot.example.com"), "5.20.0.10.in-addr.arpa", ""},
		{form("type", "PTR", "name", "10.9.20.5", "value", "cam.iot.example.com"), "", "outside"},
		{form("type", "TXT", "name", "bad name!", "value", "x"), "", "not a valid name"},
		{form("type", "MX", "name", "x", "value", "x"), "", "Choose one of"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("POST", "/", strings.NewReader(c.form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req, err := buildRecordRequest(n, "iot.example.com", r)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%v: err = %v, want %q", c.form, err, c.wantErr)
			}
			continue
		}
		if err != nil || req.Domain != c.domain {
			t.Errorf("%v: domain = %q, err = %v; want %q", c.form, req.Domain, err, c.domain)
		}
	}
}

func TestReverseDomain(t *testing.T) {
	for subnet, want := range map[string]string{
		"10.0.20.1/24": "20.0.10.in-addr.arpa",
		"10.0.20.1/26": "20.0.10.in-addr.arpa",
		"10.4.0.0/14":  "10.in-addr.arpa",
		"10.4.0.0/16":  "4.10.in-addr.arpa",
	} {
		_, n, _ := net.ParseCIDR(subnet)
		if got, err := reverseDomain(n); err != nil || got != want {
			t.Errorf("%s: %q, %v; want %q", subnet, got, err, want)
		}
	}
}
