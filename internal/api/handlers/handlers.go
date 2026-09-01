// Package handlers implements the authenticated dashboard: viewing devices,
// editing hostname overrides, excluding devices from DNS sync, forgetting
// devices, triggering a manual sync, viewing the sync/audit log, and (in
// settings.go) the admin-only connection Settings pages.
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

// deviceView pairs a Device with the name of the Identity it belongs to (if
// any), so the dashboard/device pages can show "part of <identity>" instead
// of the device's own (intentionally unsynced) DNS status.
type deviceView struct {
	db.Device
	IdentityName string
}

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

// DNSClientFactory builds a DNSClient from the Technitium connection
// currently stored in the DB, so it always reflects the latest Settings
// edits rather than what was configured at process start.
type DNSClientFactory func(ctx context.Context) (DNSClient, error)

type Handlers struct {
	DB          *gorm.DB
	Engine      Engine
	DNS         DNSClientFactory
	Pages       map[string]*template.Template
	Logger      *slog.Logger
	CurrentUser func(*http.Request) string
	IsAdmin     func(*http.Request) bool
}

type pageData struct {
	Title   string
	User    string
	IsAdmin bool
	Flash   string

	Devices []deviceView
	Events  []db.SyncEvent

	// device.html
	Device deviceView

	// events.html filters
	Actors      []string
	FilterMAC   string
	FilterActor string

	// settings_*.html
	AppSettings     db.AppSettings
	ControllerViews []controllerView
	ZoneNames       []string
	TechSettings    db.TechnitiumSettings
	Zones           []technitium.ZoneInfo

	// settings_identities.html
	IdentityViews []identityView
	UnclaimedMACs []string
}

// Routes returns the authenticated dashboard's routes. Callers are
// responsible for wrapping this with auth middleware.
func (h *Handlers) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", h.dashboard)
	mux.HandleFunc("GET /events", h.events)
	mux.HandleFunc("POST /sync", h.triggerSync)
	mux.HandleFunc("GET /devices/{id}", h.deviceDetail)
	mux.HandleFunc("POST /devices/{id}/override", h.setOverride)
	mux.HandleFunc("POST /devices/{id}/exclude", h.toggleExclude)
	mux.HandleFunc("POST /devices/{id}/forget", h.forgetDevice)
	return mux
}

func (h *Handlers) render(w http.ResponseWriter, r *http.Request, page string, data pageData) {
	data.User = h.CurrentUser(r)
	data.IsAdmin = h.IsAdmin(r)
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

// logAudit records an admin-driven action taken from the dashboard, so it
// shows up in the same unified audit log the sync engine writes to.
func (h *Handlers) logAudit(r *http.Request, mac string, action db.SyncEventAction, detail string, success bool) {
	writeAuditEvent(h.DB, h.Logger, mac, h.CurrentUser(r), action, detail, success)
}

func writeAuditEvent(gdb *gorm.DB, logger *slog.Logger, mac, actor string, action db.SyncEventAction, detail string, success bool) {
	if actor == "" {
		actor = db.SystemActor
	}
	event := db.SyncEvent{
		MAC:       mac,
		Action:    action,
		Detail:    detail,
		Success:   success,
		Actor:     actor,
		CreatedAt: time.Now(),
	}
	if err := gdb.Create(&event).Error; err != nil {
		logger.Error("failed to record audit event", "error", err)
	}
}

func (h *Handlers) dashboard(w http.ResponseWriter, r *http.Request) {
	var devices []db.Device
	if err := h.DB.Order("hostname").Find(&devices).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	names, err := h.identityNames(devices)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	views := make([]deviceView, 0, len(devices))
	for _, d := range devices {
		view := deviceView{Device: d}
		if d.IdentityID != nil {
			view.IdentityName = names[*d.IdentityID]
		}
		views = append(views, view)
	}
	h.render(w, r, "dashboard", pageData{
		Title:   "Devices",
		Devices: views,
		Flash:   r.URL.Query().Get("flash"),
	})
}

// identityNames looks up the Identity name for every non-nil IdentityID
// among devices, keyed by identity ID for cheap lookup while rendering.
func (h *Handlers) identityNames(devices []db.Device) (map[uint]string, error) {
	ids := make([]uint, 0)
	seen := make(map[uint]bool)
	for _, d := range devices {
		if d.IdentityID != nil && !seen[*d.IdentityID] {
			seen[*d.IdentityID] = true
			ids = append(ids, *d.IdentityID)
		}
	}
	names := make(map[uint]string)
	if len(ids) == 0 {
		return names, nil
	}
	var identities []db.Identity
	if err := h.DB.Where("id IN ?", ids).Find(&identities).Error; err != nil {
		return nil, err
	}
	for _, i := range identities {
		names[i.ID] = i.Name
	}
	return names, nil
}

func (h *Handlers) deviceDetail(w http.ResponseWriter, r *http.Request) {
	dev, ok := h.deviceByID(w, r)
	if !ok {
		return
	}
	var events []db.SyncEvent
	if err := h.DB.Where("mac = ?", dev.MAC).Order("created_at desc").Limit(200).Find(&events).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	view := deviceView{Device: dev}
	if dev.IdentityID != nil {
		var ident db.Identity
		if err := h.DB.First(&ident, *dev.IdentityID).Error; err == nil {
			view.IdentityName = ident.Name
		}
	}
	h.render(w, r, "device", pageData{
		Title:  dev.EffectiveHostname(),
		Device: view,
		Events: events,
	})
}

func (h *Handlers) events(w http.ResponseWriter, r *http.Request) {
	macFilter := r.URL.Query().Get("mac")
	actorFilter := r.URL.Query().Get("actor")

	q := h.DB.Order("created_at desc").Limit(200)
	if macFilter != "" {
		q = q.Where("mac = ?", macFilter)
	}
	if actorFilter != "" {
		q = q.Where("actor = ?", actorFilter)
	}

	var events []db.SyncEvent
	if err := q.Find(&events).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var actors []string
	if err := h.DB.Model(&db.SyncEvent{}).Distinct().Order("actor").Pluck("actor", &actors).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	h.render(w, r, "events", pageData{
		Title:       "Sync Log",
		Events:      events,
		Actors:      actors,
		FilterMAC:   macFilter,
		FilterActor: actorFilter,
	})
}

func (h *Handlers) triggerSync(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	flash := "Sync completed."
	success := true
	if err := h.Engine.RunOnce(ctx); err != nil {
		h.Logger.Error("manual sync failed", "error", err)
		flash = "Sync failed: " + err.Error()
		success = false
	}
	h.logAudit(r, "", db.SyncEventManualSync, flash, success)
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
	detail := "cleared hostname override"
	if hostname == "" {
		dev.OverrideHostname = nil
	} else {
		dev.OverrideHostname = &hostname
		detail = "set hostname override to " + hostname
	}
	// Force the next sync cycle to (re)apply this device's DNS record under
	// the new hostname.
	dev.DNSRecordSynced = false

	if err := h.DB.Save(&dev).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.logAudit(r, dev.MAC, db.SyncEventOverride, detail, true)
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
	action, detail := db.SyncEventExclude, "excluded from DNS sync"
	if !dev.Excluded {
		action, detail = db.SyncEventInclude, "re-included in DNS sync"
	}
	h.logAudit(r, dev.MAC, action, detail, true)
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

	if dev.DNSRecordSynced && !dev.Excluded && dev.Zone != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		dns, err := h.DNS(ctx)
		if err != nil {
			h.Logger.Error("failed to build technitium client to forget device", "mac", dev.MAC, "error", err)
		} else {
			domain := dev.EffectiveHostname() + "." + dev.Zone
			if err := dns.DeleteRecord(ctx, technitium.DeleteRecordRequest{
				Domain:    domain,
				Zone:      dev.Zone,
				Type:      "A",
				IPAddress: dev.IPAddress,
			}); err != nil {
				h.Logger.Error("failed to delete DNS record for forgotten device", "mac", dev.MAC, "domain", domain, "error", err)
			}
		}
	}

	if err := h.DB.Delete(&dev).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.logAudit(r, dev.MAC, db.SyncEventForget, "forgot device "+dev.EffectiveHostname(), true)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
