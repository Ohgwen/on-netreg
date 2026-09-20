// Package handlers implements the authenticated dashboard: viewing devices,
// editing hostname overrides, excluding devices from DNS sync, forgetting
// devices, triggering a manual sync, viewing the sync/audit log, and (in
// settings.go) the admin-only connection Settings pages.
package handlers

import (
	"context"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/Ohgwen/on-netreg/internal/db"
	"github.com/Ohgwen/on-netreg/internal/directory"
	"github.com/Ohgwen/on-netreg/internal/macaddr"
	"github.com/Ohgwen/on-netreg/internal/technitium"
)

// deviceView pairs a Device with the name of the Identity it belongs to (if
// any), so the dashboard/device pages can show "part of <identity>" instead
// of the device's own (intentionally unsynced) DNS status.
type deviceView struct {
	db.Device
	IdentityName string
	// ControllerName is the UniFi console the device was last seen on; blank
	// for a pre-registered device no console has reported yet.
	ControllerName string
	// PrivateMAC reports whether this device's MAC address is locally
	// administered (a randomized "private" address), computed fresh from
	// the MAC on every render so it applies to devices tracked before this
	// check existed too, not just newly-created ones.
	PrivateMAC bool
}

func newDeviceView(d db.Device, identityName string) deviceView {
	return deviceView{
		Device:       d,
		IdentityName: identityName,
		PrivateMAC:   macaddr.IsPrivate(d.MAC),
	}
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
	// Directory is the LDAP user directory devices can be assigned to;
	// nil when LDAP isn't configured (owner assignment is then disabled).
	Directory directory.Directory
}

type pageData struct {
	Title   string
	User    string
	IsAdmin bool
	Flash   string

	Devices []deviceView
	Events  []db.SyncEvent

	// device.html
	Device  deviceView
	Aliases []aliasView

	// dashboard.html filters
	Search            string
	FilterNetwork     string
	FilterConsole     string
	Consoles          []db.UnifiController
	FilterZone        string
	FilterCreatedFrom string
	FilterCreatedTo   string
	FilterSeenFrom    string
	FilterSeenTo      string
	Networks          []string
	DeviceZones       []string

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

	// register.html / owner assignment
	LDAPEnabled bool
	FormMAC     string
	FormHost    string
	FormOwner   string
	FormError   string

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
	mux.HandleFunc("POST /devices/bulk", h.bulkAction)
	h.registerRoutes(mux)
	h.aliasRoutes(mux)
	return mux
}

func (h *Handlers) render(w http.ResponseWriter, r *http.Request, page string, data pageData) {
	data.User = h.CurrentUser(r)
	data.IsAdmin = h.IsAdmin(r)
	data.LDAPEnabled = h.Directory != nil
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

// dateFilterLayout is the format of the dashboard's date-range filter
// inputs (HTML <input type="date"> values), and of the day-precision dates
// echoed back into them.
const dateFilterLayout = "2006-01-02"

func (h *Handlers) dashboard(w http.ResponseWriter, r *http.Request) {
	search := strings.TrimSpace(r.URL.Query().Get("q"))
	networkFilter := r.URL.Query().Get("network")
	zoneFilter := r.URL.Query().Get("zone")
	consoleFilter := r.URL.Query().Get("console")
	createdFrom := r.URL.Query().Get("created_from")
	createdTo := r.URL.Query().Get("created_to")
	seenFrom := r.URL.Query().Get("seen_from")
	seenTo := r.URL.Query().Get("seen_to")

	query := h.DB.Order("hostname")
	if search != "" {
		like := "%" + search + "%"
		query = query.Where("hostname LIKE ? OR mac LIKE ? OR ip_address LIKE ? OR uni_fi_name LIKE ? OR owner_username LIKE ? OR owner_name LIKE ?", like, like, like, like, like, like)
	}
	if networkFilter != "" {
		query = query.Where("uni_fi_network_name = ?", networkFilter)
	}
	if zoneFilter != "" {
		query = query.Where("zone = ?", zoneFilter)
	}
	if id, err := strconv.ParseUint(consoleFilter, 10, 64); err == nil {
		query = query.Where("controller_id = ?", uint(id))
	}
	if t, ok := parseFilterDate(createdFrom); ok {
		query = query.Where("created_at >= ?", t)
	}
	if t, ok := parseFilterDate(createdTo); ok {
		query = query.Where("created_at < ?", t.AddDate(0, 0, 1))
	}
	if t, ok := parseFilterDate(seenFrom); ok {
		query = query.Where("last_seen >= ?", t)
	}
	if t, ok := parseFilterDate(seenTo); ok {
		query = query.Where("last_seen < ?", t.AddDate(0, 0, 1))
	}

	var devices []db.Device
	if err := query.Find(&devices).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	names, err := h.identityNames(devices)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var consoles []db.UnifiController
	if err := h.DB.Order("name").Find(&consoles).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	consoleNames := make(map[uint]string, len(consoles))
	for _, c := range consoles {
		consoleNames[c.ID] = c.Name
	}
	views := make([]deviceView, 0, len(devices))
	for _, d := range devices {
		var identityName string
		if d.IdentityID != nil {
			identityName = names[*d.IdentityID]
		}
		v := newDeviceView(d, identityName)
		v.ControllerName = consoleNames[d.ControllerID]
		views = append(views, v)
	}

	var networks []string
	if err := h.DB.Model(&db.Device{}).Where("uni_fi_network_name <> ''").Distinct().Order("uni_fi_network_name").Pluck("uni_fi_network_name", &networks).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var zones []string
	if err := h.DB.Model(&db.Device{}).Where("zone <> ''").Distinct().Order("zone").Pluck("zone", &zones).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	h.render(w, r, "dashboard", pageData{
		Title:             "Devices",
		Devices:           views,
		Flash:             r.URL.Query().Get("flash"),
		Search:            search,
		FilterNetwork:     networkFilter,
		FilterConsole:     consoleFilter,
		Consoles:          consoles,
		FilterZone:        zoneFilter,
		FilterCreatedFrom: createdFrom,
		FilterCreatedTo:   createdTo,
		FilterSeenFrom:    seenFrom,
		FilterSeenTo:      seenTo,
		Networks:          networks,
		DeviceZones:       zones,
	})
}

// parseFilterDate parses a dashboard date-range filter value, ignoring
// blank/malformed input rather than erroring the whole page.
func parseFilterDate(v string) (time.Time, bool) {
	if v == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(dateFilterLayout, v)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
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
	var identityName string
	if dev.IdentityID != nil {
		var ident db.Identity
		if err := h.DB.First(&ident, *dev.IdentityID).Error; err == nil {
			identityName = ident.Name
		}
	}
	aliases, err := h.aliasViews(dev)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	view := newDeviceView(dev, identityName)
	if dev.ControllerID != 0 {
		var ctrl db.UnifiController
		if err := h.DB.First(&ctrl, dev.ControllerID).Error; err == nil {
			view.ControllerName = ctrl.Name
		}
	}
	h.render(w, r, "device", pageData{
		Title:   dev.EffectiveHostname(),
		Device:  view,
		Aliases: aliases,
		Events:  events,
		Flash:   r.URL.Query().Get("flash"),
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
	if err := h.setExcluded(r, dev, !dev.Excluded); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// setExcluded updates a device's Excluded flag and records the audit event.
func (h *Handlers) setExcluded(r *http.Request, dev db.Device, excluded bool) error {
	dev.Excluded = excluded
	if err := h.DB.Save(&dev).Error; err != nil {
		return err
	}
	action, detail := db.SyncEventExclude, "excluded from DNS sync"
	if !excluded {
		action, detail = db.SyncEventInclude, "re-included in DNS sync"
	}
	h.logAudit(r, dev.MAC, action, detail, true)
	return nil
}

// bulkAction applies one action (include/exclude/forget) to every device ID
// posted from the dashboard's bulk-select checkboxes.
func (h *Handlers) bulkAction(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	action := r.FormValue("action")
	ids := r.Form["ids"]
	if len(ids) == 0 {
		http.Redirect(w, r, "/?flash="+url.QueryEscape("No devices selected."), http.StatusSeeOther)
		return
	}

	// Resolve the bulk owner once so a bad username fails the whole action
	// up front instead of once per device.
	bulkOwner := strings.TrimSpace(r.FormValue("owner"))
	if action == "assign" {
		if bulkOwner == "" {
			http.Redirect(w, r, "/?flash="+url.QueryEscape("Enter a username to assign."), http.StatusSeeOther)
			return
		}
		if _, err := h.lookupOwner(r.Context(), bulkOwner); err != nil {
			http.Redirect(w, r, "/?flash="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		}
	}

	count := 0
	for _, idStr := range ids {
		id, err := strconv.ParseUint(idStr, 10, 64)
		if err != nil {
			continue
		}
		var dev db.Device
		if err := h.DB.First(&dev, uint(id)).Error; err != nil {
			continue
		}
		switch action {
		case "include":
			if err := h.setExcluded(r, dev, false); err == nil {
				count++
			}
		case "exclude":
			if err := h.setExcluded(r, dev, true); err == nil {
				count++
			}
		case "forget":
			if err := h.forgetOne(r, dev); err == nil {
				count++
			}
		case "assign", "unassign":
			owner := ""
			if action == "assign" {
				owner = bulkOwner
			}
			if _, err := h.assignOwner(r, dev, owner); err == nil {
				count++
			}
		}
	}

	flash := fmt.Sprintf("Bulk %s applied to %d device(s).", action, count)
	http.Redirect(w, r, "/?flash="+url.QueryEscape(flash), http.StatusSeeOther)
}

// forgetDevice removes a device from the registry entirely and, if it had a
// synced DNS record, deletes that record immediately rather than waiting on
// the sync engine's absence-based cleanup.
func (h *Handlers) forgetDevice(w http.ResponseWriter, r *http.Request) {
	dev, ok := h.deviceByID(w, r)
	if !ok {
		return
	}
	if err := h.forgetOne(r, dev); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// forgetOne removes a device from the registry entirely and, if it had a
// synced DNS record, deletes that record immediately rather than waiting on
// the sync engine's absence-based cleanup.
func (h *Handlers) forgetOne(r *http.Request, dev db.Device) error {
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

	// Take the device's CNAME aliases down with it.
	var aliases []db.DeviceAlias
	if err := h.DB.Where("device_id = ?", dev.ID).Find(&aliases).Error; err != nil {
		return err
	}
	for _, a := range aliases {
		if a.Synced {
			if err := h.deleteAliasRecord(r.Context(), a); err != nil {
				h.Logger.Error("failed to delete CNAME for forgotten device", "mac", dev.MAC, "name", a.SyncedName, "error", err)
			}
		}
	}
	if err := h.DB.Where("device_id = ?", dev.ID).Delete(&db.DeviceAlias{}).Error; err != nil {
		return err
	}

	if err := h.DB.Delete(&dev).Error; err != nil {
		return err
	}
	h.logAudit(r, dev.MAC, db.SyncEventForget, "forgot device "+dev.EffectiveHostname(), true)
	return nil
}
