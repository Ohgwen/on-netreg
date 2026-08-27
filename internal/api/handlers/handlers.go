// Package handlers implements the authenticated dashboard: viewing devices,
// editing hostname overrides, excluding devices from DNS sync, forgetting
// devices, triggering a manual sync, and viewing the sync event log.
package handlers

import (
	"context"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"gorm.io/gorm"

	"github.com/Ohgwen/on-netreg/internal/db"
	"github.com/Ohgwen/on-netreg/internal/technitium"
)

// Engine is the subset of sync.Engine the handlers depend on.
type Engine interface {
	RunOnce(ctx context.Context) error
}

// DNSClient is the subset of the Technitium client used to remove a
// forgotten device's record immediately rather than waiting on the next
// sync cycle's delete-on-absence logic.
type DNSClient interface {
	DeleteRecord(ctx context.Context, r technitium.DeleteRecordRequest) error
}

type Handlers struct {
	DB          *gorm.DB
	Engine      Engine
	DNS         DNSClient
	Zone        string
	Pages       map[string]*template.Template
	Logger      *slog.Logger
	CurrentUser func(*http.Request) string
}

type pageData struct {
	Title   string
	User    string
	Devices []db.Device
	Events  []db.SyncEvent
	Flash   string
}

// Routes returns the authenticated dashboard's routes. Callers are
// responsible for wrapping this with auth middleware.
func (h *Handlers) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", h.dashboard)
	mux.HandleFunc("GET /events", h.events)
	mux.HandleFunc("POST /sync", h.triggerSync)
	mux.HandleFunc("POST /devices/{id}/override", h.setOverride)
	mux.HandleFunc("POST /devices/{id}/exclude", h.toggleExclude)
	mux.HandleFunc("POST /devices/{id}/forget", h.forgetDevice)
	return mux
}

func (h *Handlers) render(w http.ResponseWriter, r *http.Request, page string, data pageData) {
	data.User = h.CurrentUser(r)
	tmpl, ok := h.Pages[page]
	if !ok {
		http.Error(w, "template not found: "+page, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "layout", data); err != nil {
		h.Logger.Error("rendering template", "page", page, "error", err)
	}
}

func (h *Handlers) dashboard(w http.ResponseWriter, r *http.Request) {
	var devices []db.Device
	if err := h.DB.Order("hostname").Find(&devices).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.render(w, r, "dashboard", pageData{
		Title:   "Devices",
		Devices: devices,
		Flash:   r.URL.Query().Get("flash"),
	})
}

func (h *Handlers) events(w http.ResponseWriter, r *http.Request) {
	var events []db.SyncEvent
	if err := h.DB.Order("created_at desc").Limit(200).Find(&events).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.render(w, r, "events", pageData{Title: "Sync Log", Events: events})
}

func (h *Handlers) triggerSync(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	flash := "Sync completed."
	if err := h.Engine.RunOnce(ctx); err != nil {
		h.Logger.Error("manual sync failed", "error", err)
		flash = "Sync failed: " + err.Error()
	}
	http.Redirect(w, r, "/?flash="+url.QueryEscape(flash), http.StatusSeeOther)
}

func (h *Handlers) deviceByID(w http.ResponseWriter, r *http.Request) (db.Device, bool) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid device id", http.StatusBadRequest)
		return db.Device{}, false
	}
	var dev db.Device
	if err := h.DB.First(&dev, uint(id)).Error; err != nil {
		http.Error(w, "device not found", http.StatusNotFound)
		return db.Device{}, false
	}
	return dev, true
}

func (h *Handlers) setOverride(w http.ResponseWriter, r *http.Request) {
	dev, ok := h.deviceByID(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	hostname := r.FormValue("hostname")
	if hostname == "" {
		dev.OverrideHostname = nil
	} else {
		dev.OverrideHostname = &hostname
	}
	// Force the next sync cycle to (re)apply this device's DNS record under
	// the new hostname.
	dev.DNSRecordSynced = false

	if err := h.DB.Save(&dev).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *Handlers) toggleExclude(w http.ResponseWriter, r *http.Request) {
	dev, ok := h.deviceByID(w, r)
	if !ok {
		return
	}
	dev.Excluded = !dev.Excluded
	if err := h.DB.Save(&dev).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// forgetDevice removes a device from the registry entirely and, if it had a
// synced DNS record, deletes that record immediately rather than waiting on
// the sync engine's absence-based cleanup.
func (h *Handlers) forgetDevice(w http.ResponseWriter, r *http.Request) {
	dev, ok := h.deviceByID(w, r)
	if !ok {
		return
	}

	if dev.DNSRecordSynced && !dev.Excluded {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		domain := dev.EffectiveHostname() + "." + h.Zone
		if err := h.DNS.DeleteRecord(ctx, technitium.DeleteRecordRequest{
			Domain:    domain,
			Zone:      h.Zone,
			Type:      "A",
			IPAddress: dev.IPAddress,
		}); err != nil {
			h.Logger.Error("failed to delete DNS record for forgotten device", "mac", dev.MAC, "domain", domain, "error", err)
		}
	}

	if err := h.DB.Delete(&dev).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
