// Package web embeds and parses the dashboard's HTML templates and static
// assets so the compiled binary is self-contained.
package web

import (
	"embed"
	"html/template"
	"io/fs"
	"time"
)

//go:embed templates
var templatesFS embed.FS

//go:embed static
var staticFS embed.FS

var funcMap = template.FuncMap{
	"datetime": func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.Local().Format("2006-01-02 15:04:05")
	},
	"datetimePtr": func(t *time.Time) string {
		if t == nil || t.IsZero() {
			return ""
		}
		return t.Local().Format("2006-01-02 15:04:05")
	},
}

// pages lists the content templates that get combined with layout.html.
var pages = []string{
	"dashboard", "events", "device",
	"settings_general", "settings_controllers", "settings_technitium", "settings_zones",
}

// Templates parses each page template together with the shared layout,
// keyed by page name (e.g. "dashboard"). Each is executed via
// ExecuteTemplate(w, "layout", data).
func Templates() (map[string]*template.Template, error) {
	out := make(map[string]*template.Template, len(pages))
	for _, name := range pages {
		t, err := template.New("layout").Funcs(funcMap).ParseFS(templatesFS,
			"templates/layout.html", "templates/"+name+".html")
		if err != nil {
			return nil, err
		}
		out[name] = t
	}
	return out, nil
}

// Static returns the embedded static asset tree rooted at "static/".
func Static() (fs.FS, error) {
	return fs.Sub(staticFS, "static")
}
