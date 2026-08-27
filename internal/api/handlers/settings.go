package handlers

import (
	"context"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"gorm.io/gorm"

	"github.com/Ohgwen/on-netreg/internal/config"
	"github.com/Ohgwen/on-netreg/internal/db"
	"github.com/Ohgwen/on-netreg/internal/settings"
	"github.com/Ohgwen/on-netreg/internal/technitium"
	"github.com/Ohgwen/on-netreg/internal/unifi"
)

// controllerView pairs a UnifiController with the networks discovered on
// it, for rendering the controllers settings page.
type controllerView struct {
	db.UnifiController
	Networks []db.UnifiNetwork
}

// SettingsHandlers implements the admin-only Settings pages: general sync
// defaults, UniFi controller connections and their network->zone mappings,
// the Technitium connection, and zone management. Callers are responsible
// for wrapping Routes() with both auth and admin-group middleware.
type SettingsHandlers struct {
	DB          *gorm.DB
	SecretKey   []byte
	Pages       map[string]*template.Template
	Logger      *slog.Logger
	CurrentUser func(*http.Request) string
	IsAdmin     func(*http.Request) bool
}

func (h *SettingsHandlers) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /settings/", h.general)
	mux.HandleFunc("POST /settings/general", h.saveGeneral)

	mux.HandleFunc("GET /settings/controllers", h.controllers)
	mux.HandleFunc("POST /settings/controllers", h.addController)
	mux.HandleFunc("POST /settings/controllers/{id}", h.editController)
	mux.HandleFunc("POST /settings/controllers/{id}/delete", h.deleteController)
	mux.HandleFunc("POST /settings/controllers/{id}/test", h.testController)
	mux.HandleFunc("POST /settings/controllers/{id}/refresh-networks", h.refreshNetworks)

	mux.HandleFunc("POST /settings/networks/{id}/zone", h.setNetworkZone)

	mux.HandleFunc("GET /settings/technitium", h.technitiumPage)
	mux.HandleFunc("POST /settings/technitium", h.saveTechnitium)
	mux.HandleFunc("POST /settings/technitium/test", h.testTechnitium)

	mux.HandleFunc("GET /settings/zones", h.zonesPage)
	mux.HandleFunc("POST /settings/zones/create", h.createZone)

	return mux
}

func (h *SettingsHandlers) render(w http.ResponseWriter, r *http.Request, page string, data pageData) {
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

func (h *SettingsHandlers) logAudit(r *http.Request, action db.SyncEventAction, detail string) {
	writeAuditEvent(h.DB, h.Logger, "", h.CurrentUser(r), action, detail, true)
}

func (h *SettingsHandlers) technitiumClient() (*technitium.Client, error) {
	cfg, err := settings.LoadTechnitium(h.DB, h.SecretKey)
	if err != nil {
		return nil, err
	}
	return technitium.New(cfg), nil
}

// liveZoneNames best-effort fetches the current zone list from Technitium,
// for populating a zone <select>. A connection failure just means the
// select falls back to free text -- it isn't fatal to rendering the page.
func (h *SettingsHandlers) liveZoneNames(ctx context.Context) []string {
	client, err := h.technitiumClient()
	if err != nil {
		return nil
	}
	zones, err := client.ListZones(ctx)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(zones))
	for _, z := range zones {
		names = append(names, z.Name)
	}
	return names
}

// ---- General ----

func (h *SettingsHandlers) general(w http.ResponseWriter, r *http.Request) {
	appSettings, err := settings.LoadApp(h.DB)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.render(w, r, "settings_general", pageData{
		Title:       "Settings · General",
		AppSettings: appSettings,
		ZoneNames:   h.liveZoneNames(r.Context()),
		Flash:       r.URL.Query().Get("flash"),
	})
}

func (h *SettingsHandlers) saveGeneral(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	removeDays, _ := strconv.Atoi(r.FormValue("remove_after_absence_days"))
	pollInterval, err := time.ParseDuration(r.FormValue("poll_interval"))
	if err != nil || pollInterval <= 0 {
		pollInterval = config.Defaults().Unifi.PollInterval
	}

	row := db.AppSettings{
		DefaultZone:            r.FormValue("default_zone"),
		FallbackPattern:        r.FormValue("fallback_pattern"),
		RemoveAfterAbsenceDays: removeDays,
		PollInterval:           pollInterval,
	}
	if err := settings.SaveApp(h.DB, &row); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.logAudit(r, db.SyncEventSettingsChange, "updated general settings")
	http.Redirect(w, r, "/settings/?flash="+url.QueryEscape("Settings saved. The poll interval takes effect after a restart."), http.StatusSeeOther)
}

// ---- UniFi controllers ----

func (h *SettingsHandlers) controllers(w http.ResponseWriter, r *http.Request) {
	var rows []db.UnifiController
	if err := h.DB.Order("name").Find(&rows).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var networks []db.UnifiNetwork
	if err := h.DB.Order("name").Find(&networks).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	byController := make(map[uint][]db.UnifiNetwork, len(rows))
	for _, n := range networks {
		byController[n.ControllerID] = append(byController[n.ControllerID], n)
	}

	views := make([]controllerView, 0, len(rows))
	for _, c := range rows {
		views = append(views, controllerView{UnifiController: c, Networks: byController[c.ID]})
	}

	h.render(w, r, "settings_controllers", pageData{
		Title:           "Settings · UniFi Controllers",
		ControllerViews: views,
		ZoneNames:       h.liveZoneNames(r.Context()),
		Flash:           r.URL.Query().Get("flash"),
	})
}

func (h *SettingsHandlers) controllerByID(w http.ResponseWriter, r *http.Request) (db.UnifiController, bool) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid controller id", http.StatusBadRequest)
		return db.UnifiController{}, false
	}
	var ctrl db.UnifiController
	if err := h.DB.First(&ctrl, uint(id)).Error; err != nil {
		http.Error(w, "controller not found", http.StatusNotFound)
		return db.UnifiController{}, false
	}
	return ctrl, true
}

func (h *SettingsHandlers) addController(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctrl := db.UnifiController{
		Name:               r.FormValue("name"),
		BaseURL:            r.FormValue("base_url"),
		Username:           r.FormValue("username"),
		Site:               r.FormValue("site"),
		InsecureSkipVerify: r.FormValue("insecure_skip_verify") == "on",
		DefaultZone:        r.FormValue("default_zone"),
		Enabled:            true,
	}
	if ctrl.Site == "" {
		ctrl.Site = "default"
	}
	if err := settings.SaveController(h.DB, h.SecretKey, &ctrl, r.FormValue("password")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.logAudit(r, db.SyncEventSettingsChange, fmt.Sprintf("added unifi controller %q", ctrl.Name))
	http.Redirect(w, r, "/settings/controllers?flash="+url.QueryEscape("Controller added."), http.StatusSeeOther)
}

func (h *SettingsHandlers) editController(w http.ResponseWriter, r *http.Request) {
	ctrl, ok := h.controllerByID(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ctrl.Name = r.FormValue("name")
	ctrl.BaseURL = r.FormValue("base_url")
	ctrl.Username = r.FormValue("username")
	ctrl.Site = r.FormValue("site")
	ctrl.InsecureSkipVerify = r.FormValue("insecure_skip_verify") == "on"
	ctrl.DefaultZone = r.FormValue("default_zone")
	ctrl.Enabled = r.FormValue("enabled") == "on"

	if err := settings.SaveController(h.DB, h.SecretKey, &ctrl, r.FormValue("password")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.logAudit(r, db.SyncEventSettingsChange, fmt.Sprintf("updated unifi controller %q", ctrl.Name))
	http.Redirect(w, r, "/settings/controllers?flash="+url.QueryEscape("Controller updated."), http.StatusSeeOther)
}

func (h *SettingsHandlers) deleteController(w http.ResponseWriter, r *http.Request) {
	ctrl, ok := h.controllerByID(w, r)
	if !ok {
		return
	}
	if err := h.DB.Delete(&ctrl).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.logAudit(r, db.SyncEventSettingsChange, fmt.Sprintf("deleted unifi controller %q", ctrl.Name))
	http.Redirect(w, r, "/settings/controllers?flash="+url.QueryEscape("Controller deleted."), http.StatusSeeOther)
}

func (h *SettingsHandlers) controllerClient(ctrl db.UnifiController) (*unifi.API, error) {
	password, err := settings.Decrypt(h.SecretKey, ctrl.PasswordEncrypted)
	if err != nil {
		return nil, fmt.Errorf("decrypting controller password: %w", err)
	}
	return unifi.New(config.UnifiConfig{
		BaseURL:            ctrl.BaseURL,
		Username:           ctrl.Username,
		Password:           password,
		Site:               ctrl.Site,
		InsecureSkipVerify: ctrl.InsecureSkipVerify,
	})
}

func (h *SettingsHandlers) testController(w http.ResponseWriter, r *http.Request) {
	ctrl, ok := h.controllerByID(w, r)
	if !ok {
		return
	}
	flash := "Connection OK."
	client, err := h.controllerClient(ctrl)
	if err != nil {
		flash = "Connection failed: " + err.Error()
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		if _, ferr := client.FetchClients(ctx); ferr != nil {
			flash = "Connection failed: " + ferr.Error()
		}
	}
	http.Redirect(w, r, "/settings/controllers?flash="+url.QueryEscape(flash), http.StatusSeeOther)
}

func (h *SettingsHandlers) refreshNetworks(w http.ResponseWriter, r *http.Request) {
	ctrl, ok := h.controllerByID(w, r)
	if !ok {
		return
	}
	client, err := h.controllerClient(ctrl)
	if err != nil {
		http.Redirect(w, r, "/settings/controllers?flash="+url.QueryEscape("Refresh failed: "+err.Error()), http.StatusSeeOther)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	networks, err := client.FetchNetworks(ctx)
	if err != nil {
		http.Redirect(w, r, "/settings/controllers?flash="+url.QueryEscape("Refresh failed: "+err.Error()), http.StatusSeeOther)
		return
	}
	if err := settings.UpsertNetworks(h.DB, ctrl.ID, networks); err != nil {
		http.Redirect(w, r, "/settings/controllers?flash="+url.QueryEscape("Refresh failed: "+err.Error()), http.StatusSeeOther)
		return
	}
	h.logAudit(r, db.SyncEventSettingsChange, fmt.Sprintf("refreshed networks for %q (%d found)", ctrl.Name, len(networks)))
	http.Redirect(w, r, "/settings/controllers?flash="+url.QueryEscape(fmt.Sprintf("Found %d networks.", len(networks))), http.StatusSeeOther)
}

// ---- Network -> zone mapping ----

func (h *SettingsHandlers) setNetworkZone(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid network id", http.StatusBadRequest)
		return
	}
	var network db.UnifiNetwork
	if err := h.DB.First(&network, uint(id)).Error; err != nil {
		http.Error(w, "network not found", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	zone := r.FormValue("zone")
	if newZone := r.FormValue("new_zone"); newZone != "" {
		client, err := h.technitiumClient()
		if err != nil {
			http.Redirect(w, r, "/settings/controllers?flash="+url.QueryEscape("Zone creation failed: "+err.Error()), http.StatusSeeOther)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		if err := client.CreateZone(ctx, newZone, "Primary"); err != nil {
			http.Redirect(w, r, "/settings/controllers?flash="+url.QueryEscape("Zone creation failed: "+err.Error()), http.StatusSeeOther)
			return
		}
		zone = newZone
	}

	network.Zone = zone
	if err := h.DB.Save(&network).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.logAudit(r, db.SyncEventSettingsChange, fmt.Sprintf("set network %q zone to %q", network.Name, zone))
	http.Redirect(w, r, "/settings/controllers?flash="+url.QueryEscape("Network zone updated."), http.StatusSeeOther)
}

// ---- Technitium connection ----

func (h *SettingsHandlers) technitiumPage(w http.ResponseWriter, r *http.Request) {
	var row db.TechnitiumSettings
	h.DB.First(&row, 1) // zero value is fine for a not-yet-configured form
	h.render(w, r, "settings_technitium", pageData{
		Title:        "Settings · Technitium",
		TechSettings: row,
		Flash:        r.URL.Query().Get("flash"),
	})
}

func (h *SettingsHandlers) saveTechnitium(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ttl, _ := strconv.Atoi(r.FormValue("ttl"))
	row := db.TechnitiumSettings{
		BaseURL:   r.FormValue("base_url"),
		Username:  r.FormValue("username"),
		TTL:       ttl,
		CreatePTR: r.FormValue("create_ptr") == "on",
	}
	if err := settings.SaveTechnitium(h.DB, h.SecretKey, &row, r.FormValue("password")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.logAudit(r, db.SyncEventSettingsChange, "updated technitium connection")
	http.Redirect(w, r, "/settings/technitium?flash="+url.QueryEscape("Saved."), http.StatusSeeOther)
}

func (h *SettingsHandlers) testTechnitium(w http.ResponseWriter, r *http.Request) {
	flash := "Connection OK."
	client, err := h.technitiumClient()
	if err != nil {
		flash = "Connection failed: " + err.Error()
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		if _, zerr := client.ListZones(ctx); zerr != nil {
			flash = "Connection failed: " + zerr.Error()
		}
	}
	http.Redirect(w, r, "/settings/technitium?flash="+url.QueryEscape(flash), http.StatusSeeOther)
}

// ---- Zones ----

func (h *SettingsHandlers) zonesPage(w http.ResponseWriter, r *http.Request) {
	flash := r.URL.Query().Get("flash")
	client, err := h.technitiumClient()
	var zones []technitium.ZoneInfo
	if err != nil {
		flash = "Technitium is not configured yet: " + err.Error()
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		zones, err = client.ListZones(ctx)
		if err != nil {
			flash = "Failed to list zones: " + err.Error()
		}
	}
	h.render(w, r, "settings_zones", pageData{
		Title: "Settings · Zones",
		Zones: zones,
		Flash: flash,
	})
}

func (h *SettingsHandlers) createZone(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	zone := r.FormValue("zone")
	zoneType := r.FormValue("type")

	flash := fmt.Sprintf("Zone %q created.", zone)
	client, err := h.technitiumClient()
	if err != nil {
		flash = "Failed: " + err.Error()
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		if err := client.CreateZone(ctx, zone, zoneType); err != nil {
			flash = "Failed to create zone: " + err.Error()
		} else {
			h.logAudit(r, db.SyncEventSettingsChange, fmt.Sprintf("created DNS zone %q", zone))
		}
	}
	http.Redirect(w, r, "/settings/zones?flash="+url.QueryEscape(flash), http.StatusSeeOther)
}
