// Package web embeds and parses the dashboard's HTML templates and static
// assets so the compiled binary is self-contained.
package web

import (
	"embed"
	"fmt"
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
	"formatDuration": func(seconds int) string {
		if seconds <= 0 {
			return ""
		}
		d := time.Duration(seconds) * time.Second
		days := int(d.Hours()) / 24
		hours := int(d.Hours()) % 24
		mins := int(d.Minutes()) % 60
		switch {
		case days > 0:
			return fmt.Sprintf("%dd %dh", days, hours)
		case hours > 0:
			return fmt.Sprintf("%dh %dm", hours, mins)
		default:
			return fmt.Sprintf("%dm", mins)
		}
	},
	// divHours converts seconds to (fractional) hours, for form values.
	"divHours": func(seconds int) float64 { return float64(seconds) / 3600 },
	// dict builds a map from alternating keys and values, so a sub-template
	// can take several arguments.
	"dict": func(kv ...any) (map[string]any, error) {
		if len(kv)%2 != 0 {
			return nil, fmt.Errorf("dict: odd number of arguments")
		}
		m := make(map[string]any, len(kv)/2)
		for i := 0; i < len(kv); i += 2 {
			k, ok := kv[i].(string)
			if !ok {
				return nil, fmt.Errorf("dict: key %v is not a string", kv[i])
			}
			m[k] = kv[i+1]
		}
		return m, nil
	},
	"formatBytes": func(n int64) string {
		if n <= 0 {
			return "0 B"
		}
		const unit = 1024
		if n < unit {
			return fmt.Sprintf("%d B", n)
		}
		div, exp := int64(unit), 0
		for m := n / unit; m >= unit; m /= unit {
			div *= unit
			exp++
		}
		return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
	},
}

// pages lists the content templates that get combined with layout.html.
var pages = []string{
	"dashboard", "events", "device", "register",
	"settings_general", "settings_controllers", "settings_technitium", "settings_zones", "settings_identities",
	"settings_networks", "settings_network",
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
