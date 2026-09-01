package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/Ohgwen/on-netreg/internal/db"
	"github.com/Ohgwen/on-netreg/internal/macaddr"
	"github.com/Ohgwen/on-netreg/internal/technitium"
)

// identityView pairs an Identity with its members, for rendering the
// Identities settings page.
type identityView struct {
	db.Identity
	Members []db.IdentityMember
}

func (h *SettingsHandlers) identitiesRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /settings/identities", h.identities)
	mux.HandleFunc("POST /settings/identities", h.addIdentity)
	mux.HandleFunc("POST /settings/identities/{id}", h.editIdentity)
	mux.HandleFunc("POST /settings/identities/{id}/delete", h.deleteIdentity)
	mux.HandleFunc("POST /settings/identities/{id}/members", h.addIdentityMember)
	mux.HandleFunc("POST /settings/identities/{id}/members/{memberID}", h.editIdentityMember)
	mux.HandleFunc("POST /settings/identities/{id}/members/{memberID}/delete", h.deleteIdentityMember)
}

func (h *SettingsHandlers) identities(w http.ResponseWriter, r *http.Request) {
	var identities []db.Identity
	if err := h.DB.Order("name").Find(&identities).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var members []db.IdentityMember
	if err := h.DB.Order("priority asc").Find(&members).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	byIdentity := make(map[uint][]db.IdentityMember, len(identities))
	claimed := make(map[string]bool, len(members))
	for _, m := range members {
		byIdentity[m.IdentityID] = append(byIdentity[m.IdentityID], m)
		claimed[m.MAC] = true
	}

	views := make([]identityView, 0, len(identities))
	for _, i := range identities {
		views = append(views, identityView{Identity: i, Members: byIdentity[i.ID]})
	}

	var devices []db.Device
	if err := h.DB.Order("hostname").Find(&devices).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	unclaimed := make([]string, 0, len(devices))
	for _, d := range devices {
		if !claimed[d.MAC] {
			unclaimed = append(unclaimed, d.MAC)
		}
	}

	h.render(w, r, "settings_identities", pageData{
		Title:         "Settings · Identities",
		IdentityViews: views,
		UnclaimedMACs: unclaimed,
		ZoneNames:     h.liveZoneNames(r.Context()),
		Flash:         r.URL.Query().Get("flash"),
	})
}

func (h *SettingsHandlers) identityByID(w http.ResponseWriter, r *http.Request) (db.Identity, bool) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid identity id", http.StatusBadRequest)
		return db.Identity{}, false
	}
	var ident db.Identity
	if err := h.DB.First(&ident, uint(id)).Error; err != nil {
		http.Error(w, "identity not found", http.StatusNotFound)
		return db.Identity{}, false
	}
	return ident, true
}

func (h *SettingsHandlers) addIdentity(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ident := db.Identity{
		Name: r.FormValue("name"),
		Zone: r.FormValue("zone"),
	}
	if ident.Name == "" || ident.Zone == "" {
		http.Redirect(w, r, "/settings/identities?flash="+url.QueryEscape("Name and zone are required."), http.StatusSeeOther)
		return
	}
	if err := h.DB.Create(&ident).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.logAudit(r, db.SyncEventSettingsChange, fmt.Sprintf("created identity %q", ident.Name))
	http.Redirect(w, r, "/settings/identities?flash="+url.QueryEscape("Identity created. Add its member MAC addresses below."), http.StatusSeeOther)
}

func (h *SettingsHandlers) editIdentity(w http.ResponseWriter, r *http.Request) {
	ident, ok := h.identityByID(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ident.Name = r.FormValue("name")
	ident.Zone = r.FormValue("zone")
	if override := r.FormValue("override_hostname"); override != "" {
		ident.OverrideHostname = &override
	} else {
		ident.OverrideHostname = nil
	}
	ident.Excluded = r.FormValue("excluded") == "on"

	if err := h.DB.Save(&ident).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.logAudit(r, db.SyncEventSettingsChange, fmt.Sprintf("updated identity %q", ident.Name))
	http.Redirect(w, r, "/settings/identities?flash="+url.QueryEscape("Identity updated."), http.StatusSeeOther)
}

// deleteIdentity removes an Identity and its members. If it currently has a
// synced DNS record, that record is deleted immediately (like forgetting a
// device) rather than left for a future sync cycle to notice and clean up.
func (h *SettingsHandlers) deleteIdentity(w http.ResponseWriter, r *http.Request) {
	ident, ok := h.identityByID(w, r)
	if !ok {
		return
	}

	if ident.DNSRecordSynced && ident.Zone != "" {
		client, err := h.technitiumClient()
		if err != nil {
			h.Logger.Error("failed to build technitium client to delete identity record", "identity", ident.Name, "error", err)
		} else {
			ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			defer cancel()
			domain := ident.EffectiveHostname() + "." + ident.Zone
			if err := client.DeleteRecord(ctx, technitium.DeleteRecordRequest{
				Domain:    domain,
				Zone:      ident.Zone,
				Type:      "A",
				IPAddress: ident.IPAddress,
			}); err != nil {
				h.Logger.Error("failed to delete DNS record for identity", "identity", ident.Name, "domain", domain, "error", err)
			}
		}
	}

	if err := h.DB.Where("identity_id = ?", ident.ID).Delete(&db.IdentityMember{}).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.DB.Model(&db.Device{}).Where("identity_id = ?", ident.ID).Update("identity_id", nil).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.DB.Delete(&ident).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.logAudit(r, db.SyncEventSettingsChange, fmt.Sprintf("deleted identity %q", ident.Name))
	http.Redirect(w, r, "/settings/identities?flash="+url.QueryEscape("Identity deleted."), http.StatusSeeOther)
}

// addIdentityMember adds a MAC address as a member of an Identity. If a
// Device row already exists for that MAC and had a synced DNS record of its
// own, that record is deleted immediately -- the identity now speaks for
// it -- rather than left stale until a sync cycle notices.
func (h *SettingsHandlers) addIdentityMember(w http.ResponseWriter, r *http.Request) {
	ident, ok := h.identityByID(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	mac := macaddr.Normalize(r.FormValue("mac"))
	if mac == "" {
		http.Redirect(w, r, "/settings/identities?flash="+url.QueryEscape("A MAC address is required."), http.StatusSeeOther)
		return
	}
	priority, _ := strconv.Atoi(r.FormValue("priority"))

	member := db.IdentityMember{
		IdentityID: ident.ID,
		MAC:        mac,
		Priority:   priority,
		Label:      r.FormValue("label"),
	}
	if err := h.DB.Create(&member).Error; err != nil {
		http.Redirect(w, r, "/settings/identities?flash="+url.QueryEscape("Failed to add member: "+err.Error()), http.StatusSeeOther)
		return
	}

	var dev db.Device
	if err := h.DB.Where("mac = ?", mac).First(&dev).Error; err == nil {
		if dev.DNSRecordSynced && !dev.Excluded && dev.Zone != "" {
			client, cerr := h.technitiumClient()
			if cerr != nil {
				h.Logger.Error("failed to build technitium client to clear device record for new identity member", "mac", mac, "error", cerr)
			} else {
				ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
				defer cancel()
				domain := dev.EffectiveHostname() + "." + dev.Zone
				if err := client.DeleteRecord(ctx, technitium.DeleteRecordRequest{
					Domain:    domain,
					Zone:      dev.Zone,
					Type:      "A",
					IPAddress: dev.IPAddress,
				}); err != nil {
					h.Logger.Error("failed to delete device's own DNS record when joining identity", "mac", mac, "domain", domain, "error", err)
				}
				dev.DNSRecordSynced = false
			}
		}
		dev.IdentityID = &ident.ID
		if err := h.DB.Save(&dev).Error; err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	h.logAudit(r, db.SyncEventSettingsChange, fmt.Sprintf("added %s to identity %q", mac, ident.Name))
	http.Redirect(w, r, "/settings/identities?flash="+url.QueryEscape("Member added."), http.StatusSeeOther)
}

func (h *SettingsHandlers) memberByID(w http.ResponseWriter, r *http.Request) (db.IdentityMember, bool) {
	id, err := strconv.ParseUint(r.PathValue("memberID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid member id", http.StatusBadRequest)
		return db.IdentityMember{}, false
	}
	var member db.IdentityMember
	if err := h.DB.First(&member, uint(id)).Error; err != nil {
		http.Error(w, "member not found", http.StatusNotFound)
		return db.IdentityMember{}, false
	}
	return member, true
}

func (h *SettingsHandlers) editIdentityMember(w http.ResponseWriter, r *http.Request) {
	member, ok := h.memberByID(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	priority, _ := strconv.Atoi(r.FormValue("priority"))
	member.Priority = priority
	member.Label = r.FormValue("label")
	if err := h.DB.Save(&member).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.logAudit(r, db.SyncEventSettingsChange, fmt.Sprintf("updated identity member %s", member.MAC))
	http.Redirect(w, r, "/settings/identities?flash="+url.QueryEscape("Member updated."), http.StatusSeeOther)
}

// deleteIdentityMember removes a MAC from an Identity. Its Device row (if
// any) goes back to being synced individually on the next cycle.
func (h *SettingsHandlers) deleteIdentityMember(w http.ResponseWriter, r *http.Request) {
	member, ok := h.memberByID(w, r)
	if !ok {
		return
	}
	if err := h.DB.Delete(&member).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.DB.Model(&db.Device{}).Where("mac = ?", member.MAC).Update("identity_id", nil).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.logAudit(r, db.SyncEventSettingsChange, fmt.Sprintf("removed %s from identity", member.MAC))
	http.Redirect(w, r, "/settings/identities?flash="+url.QueryEscape("Member removed."), http.StatusSeeOther)
}
