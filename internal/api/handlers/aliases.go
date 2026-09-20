package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/Ohgwen/on-netreg/internal/db"
	"github.com/Ohgwen/on-netreg/internal/registry"
	"github.com/Ohgwen/on-netreg/internal/technitium"
)

// aliasView is a DeviceAlias plus the FQDN it currently has (or will get)
// for display.
type aliasView struct {
	db.DeviceAlias
	FQDN string
}

func (h *Handlers) aliasRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /devices/{id}/aliases", h.addAlias)
	mux.HandleFunc("POST /devices/{id}/aliases/{aliasID}/delete", h.deleteAlias)
}

func (h *Handlers) aliasViews(dev db.Device) ([]aliasView, error) {
	var aliases []db.DeviceAlias
	if err := h.DB.Where("device_id = ?", dev.ID).Order("label").Find(&aliases).Error; err != nil {
		return nil, err
	}
	views := make([]aliasView, 0, len(aliases))
	for _, a := range aliases {
		v := aliasView{DeviceAlias: a}
		switch zone := aliasZone(a, dev); {
		case a.SyncedName != "":
			v.FQDN = a.SyncedName
		case zone != "":
			v.FQDN = a.Label + "." + zone
		default:
			v.FQDN = a.Label
		}
		views = append(views, v)
	}
	return views, nil
}

func (h *Handlers) addAlias(w http.ResponseWriter, r *http.Request) {
	dev, ok := h.deviceByID(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	flash, err := h.createAlias(r, dev, r.FormValue("label"))
	if err != nil {
		var ie inputError
		if !errors.As(err, &ie) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		flash = ie.Error()
	} else if dev.DNSRecordSynced {
		// The device already has an A record: publish the CNAME now rather
		// than on the next poll.
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		if err := h.Engine.RunOnce(ctx); err != nil {
			h.Logger.Error("sync after adding alias failed", "mac", dev.MAC, "error", err)
			flash += " Sync failed: " + err.Error()
		}
	}
	http.Redirect(w, r, deviceURL(dev)+"?flash="+url.QueryEscape(flash), http.StatusSeeOther)
}

func deviceURL(dev db.Device) string {
	return "/devices/" + strconv.FormatUint(uint64(dev.ID), 10)
}

// aliasZone is the zone an alias lives in: its own pin, else its device's
// zone (falling back to the device's pinned zone before it has connected).
func aliasZone(a db.DeviceAlias, dev db.Device) string {
	switch {
	case a.Zone != "":
		return a.Zone
	case dev.Zone != "":
		return dev.Zone
	}
	return dev.ZoneOverride
}

// parseAlias turns what the admin typed into a zone-relative label and a
// zone. A bare name ("www") lives in the device's zone (zone ""). A fully
// qualified name ("www.other.example.com") is matched against the known
// zones, longest first, and pins the alias there unless that is the
// device's own zone.
func (h *Handlers) parseAlias(ctx context.Context, dev db.Device, raw string) (label, zone string, err error) {
	name := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
	if name == "" {
		return "", "", inputError("Enter an alias name or a fully qualified domain name.")
	}
	bad := inputError("Alias names may only use letters, digits and hyphens.")

	relative := name
	if strings.Contains(name, ".") {
		candidates := h.zoneChoices(ctx)
		candidates = append(candidates, dev.Zone, dev.ZoneOverride)
		best := ""
		for _, z := range candidates {
			if z == name {
				return "", "", inputError(fmt.Sprintf("%q is a zone itself, not a name inside one.", name))
			}
			if z != "" && strings.HasSuffix(name, "."+z) && len(z) > len(best) {
				best = z
			}
		}
		if best == "" {
			return "", "", inputError(fmt.Sprintf("%q isn't in a zone known to this server. Pick a name under one of its zones.", name))
		}
		relative = strings.TrimSuffix(name, "."+best)
		if best != dev.Zone && best != dev.ZoneOverride {
			zone = best
		}
	}

	parts := strings.Split(relative, ".")
	for i, p := range parts {
		if p == "" || registry.SanitizeLabel(p) != p {
			return "", "", bad
		}
		parts[i] = p
	}
	label = strings.Join(parts, ".")
	if len(label) > 253 {
		return "", "", inputError("That name is too long.")
	}
	return label, zone, nil
}

// createAlias validates what was typed and stores a new alias for dev. The
// CNAME is published by the sync engine once the device has a DNS record.
func (h *Handlers) createAlias(r *http.Request, dev db.Device, raw string) (string, error) {
	if dev.IdentityID != nil {
		return "", inputError("That device is a member of an identity; it has no DNS name of its own to alias.")
	}
	label, zone, err := h.parseAlias(r.Context(), dev, raw)
	if err != nil {
		return "", err
	}
	alias := db.DeviceAlias{DeviceID: dev.ID, Label: label, Zone: zone, CreatedBy: h.CurrentUser(r)}
	effective := aliasZone(alias, dev)

	if label == dev.EffectiveHostname() && effective == dev.Zone {
		return "", inputError("An alias can't be the same as the device's own hostname.")
	}

	err = h.DB.Transaction(func(tx *gorm.DB) error {
		if err := aliasNameTaken(tx, alias, effective); err != nil {
			return err
		}
		if err := tx.Create(&alias).Error; err != nil {
			if isUniqueViolation(err) {
				return inputError(fmt.Sprintf("%q is already an alias of this device.", label))
			}
			return err
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	fq := label
	if effective != "" {
		fq = label + "." + effective
	}
	h.logAudit(r, dev.MAC, db.SyncEventAlias, "added CNAME alias "+fq, true)
	if !dev.DNSRecordSynced {
		return fmt.Sprintf("Added alias %s. It is published once the device has a DNS record.", fq), nil
	}
	return "Added alias " + fq + ".", nil
}

// aliasNameTaken rejects a name that would collide, within its zone, with a
// device hostname or another alias -- a CNAME can't coexist with any other
// record at the same name.
func aliasNameTaken(tx *gorm.DB, alias db.DeviceAlias, zone string) error {
	if zone == "" {
		return nil
	}
	var n int64
	if err := tx.Model(&db.Device{}).
		Where("zone = ? AND (hostname = ? OR override_hostname = ?)", zone, alias.Label, alias.Label).
		Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return inputError(fmt.Sprintf("%s.%s is already a device hostname.", alias.Label, zone))
	}
	if err := tx.Model(&db.DeviceAlias{}).
		Joins("JOIN devices ON devices.id = device_aliases.device_id").
		Where("device_aliases.label = ? AND COALESCE(NULLIF(device_aliases.zone, ''), devices.zone) = ?", alias.Label, zone).
		Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return inputError(fmt.Sprintf("%s.%s is already an alias.", alias.Label, zone))
	}
	return nil
}

func (h *Handlers) deleteAlias(w http.ResponseWriter, r *http.Request) {
	dev, ok := h.deviceByID(w, r)
	if !ok {
		return
	}
	aliasID, err := strconv.ParseUint(r.PathValue("aliasID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid alias id", http.StatusBadRequest)
		return
	}
	var alias db.DeviceAlias
	if err := h.DB.Where("id = ? AND device_id = ?", uint(aliasID), dev.ID).First(&alias).Error; err != nil {
		http.Error(w, "alias not found", http.StatusNotFound)
		return
	}

	if alias.Synced {
		if err := h.deleteAliasRecord(r.Context(), alias); err != nil {
			h.Logger.Error("failed to delete CNAME", "name", alias.SyncedName, "error", err)
			http.Redirect(w, r, deviceURL(dev)+"?flash="+url.QueryEscape("Could not remove the DNS record: "+err.Error()), http.StatusSeeOther)
			return
		}
	}
	if err := h.DB.Delete(&alias).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.logAudit(r, dev.MAC, db.SyncEventAlias, "removed CNAME alias "+alias.Label, true)
	http.Redirect(w, r, deviceURL(dev)+"?flash="+url.QueryEscape("Removed alias "+alias.Label+"."), http.StatusSeeOther)
}

// deleteAliasRecord removes an alias's CNAME from DNS immediately.
func (h *Handlers) deleteAliasRecord(ctx context.Context, a db.DeviceAlias) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	dns, err := h.DNS(ctx)
	if err != nil {
		return err
	}
	err = dns.DeleteRecord(ctx, technitium.DeleteRecordRequest{Domain: a.SyncedName, Zone: a.SyncedZone, Type: "CNAME"})
	if err != nil {
		s := strings.ToLower(err.Error())
		if strings.Contains(s, "no such") || strings.Contains(s, "not exist") || strings.Contains(s, "not found") {
			return nil
		}
	}
	return err
}
