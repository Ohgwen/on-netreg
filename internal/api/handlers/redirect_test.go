package handlers

import (
	"net/http/httptest"
	"testing"
)

func TestRedirectBackKeepsFiltersAndReplacesFlash(t *testing.T) {
	cases := []struct{ referer, want string }{
		{"http://netreg.local/?zone=iot.int&q=tv&flash=old", "/?flash=Saved&q=tv&zone=iot.int"},
		{"http://netreg.local/devices/30", "/devices/30?flash=Saved"},
		{"http://evil.example/phish", "/?flash=Saved"}, // other host: ignored
		{"", "/?flash=Saved"},
	}
	for _, c := range cases {
		req := httptest.NewRequest("POST", "http://netreg.local/devices/30/override", nil)
		if c.referer != "" {
			req.Header.Set("Referer", c.referer)
		}
		rec := httptest.NewRecorder()
		redirectBack(rec, req, "/", "Saved")
		if got := rec.Header().Get("Location"); got != c.want {
			t.Errorf("referer %q: Location = %q, want %q", c.referer, got, c.want)
		}
	}
}
