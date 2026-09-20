package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Ohgwen/on-netreg/internal/api/web"
	"github.com/Ohgwen/on-netreg/internal/db"
)

func TestPagesRender(t *testing.T) {
	h, gdb := testHandlers(t)
	pages, err := web.Templates()
	if err != nil {
		t.Fatal(err)
	}
	h.Pages = pages
	h.CurrentUser = func(*http.Request) string { return "admin" }
	h.IsAdmin = func(*http.Request) bool { return true }
	gdb.Create(&db.Device{MAC: "aa:bb:cc:dd:ee:09", Hostname: "pre", Registered: true, OwnerUsername: "alice", OwnerName: "Alice A"})

	for path, want := range map[string]string{
		"/":                  "Alice A",
		"/register":          "Assign to user",
		"/devices/1":         "Not connected yet",
		"/users/search?q=al": "[]",
	} {
		rec := httptest.NewRecorder()
		h.Routes().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 || !strings.Contains(strings.ToLower(rec.Body.String()), strings.ToLower(want)) {
			t.Errorf("GET %s = %d, missing %q\n%s", path, rec.Code, want, rec.Body.String())
		}
	}
}
