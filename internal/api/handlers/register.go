package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/Ohgwen/on-netreg/internal/db"
	"github.com/Ohgwen/on-netreg/internal/directory"
	"github.com/Ohgwen/on-netreg/internal/macaddr"
	"github.com/Ohgwen/on-netreg/internal/registry"
)

// inputError is a problem with what the admin entered (as opposed to a
// server fault), safe to show back on the form.
type inputError string

func (e inputError) Error() string { return string(e) }

func (h *Handlers) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /register", h.registerForm)
	mux.HandleFunc("POST /register", h.registerSubmit)
	mux.HandleFunc("GET /users/search", h.userSearch)
	mux.HandleFunc("POST /devices/{id}/owner", h.setOwner)
}

func (h *Handlers) registerForm(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, "register", pageData{
		Title:   "Register device",
		FormMAC: r.URL.Query().Get("mac"),
	})
}

func (h *Handlers) registerSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	mac, hostname, owner := r.FormValue("mac"), r.FormValue("hostname"), r.FormValue("owner")

	dev, existed, err := h.registerDevice(r.Context(), h.CurrentUser(r), mac, hostname, owner)
	if err != nil {
		var ie inputError
		if !errors.As(err, &ie) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusUnprocessableEntity)
		h.render(w, r, "register", pageData{
			Title:     "Register device",
			FormMAC:   mac,
			FormHost:  hostname,
			FormOwner: owner,
			FormError: ie.Error(),
		})
		return
	}

	flash := fmt.Sprintf("Registered %s as %s. It gets its DNS record when it first connects.", dev.MAC, dev.EffectiveHostname())
	if existed {
		flash = fmt.Sprintf("Updated %s: now %s.", dev.MAC, dev.EffectiveHostname())
		// Already known to a controller: push the changed record to DNS now
		// instead of waiting for the next poll.
		if dev.ControllerID != 0 {
			ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
			defer cancel()
			if err := h.Engine.RunOnce(ctx); err != nil {
				h.Logger.Error("sync after register failed", "mac", dev.MAC, "error", err)
				flash += " Sync failed: " + err.Error()
			}
		}
	}
	http.Redirect(w, r, "/?flash="+url.QueryEscape(flash), http.StatusSeeOther)
}

// registerDevice creates a device for mac (which may never have connected)
// or, if the MAC is already known, changes its hostname. Either way the
// hostname becomes the device's override, so the sync engine publishes it
// as soon as the client has an address, and the device is included in DNS
// sync. owner is an optional directory username.
func (h *Handlers) registerDevice(ctx context.Context, actor, rawMAC, rawHostname, ownerName string) (db.Device, bool, error) {
	mac, ok := macaddr.Parse(rawMAC)
	if !ok {
		return db.Device{}, false, inputError("Not a valid MAC address. Use e.g. aa:bb:cc:dd:ee:ff.")
	}
	hostname := registry.SanitizeLabel(rawHostname)
	if hostname == "" {
		return db.Device{}, false, inputError("Enter a hostname (letters, digits and hyphens).")
	}
	var owner *directory.User
	if strings.TrimSpace(ownerName) != "" {
		u, err := h.lookupOwner(ctx, ownerName)
		if err != nil {
			return db.Device{}, false, err
		}
		owner = &u
	}

	var dev db.Device
	existed := false
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		err := tx.Where("mac = ?", mac).First(&dev).Error
		switch {
		case err == nil:
			existed = true
		case errors.Is(err, gorm.ErrRecordNotFound):
			dev = db.Device{MAC: mac}
		default:
			return err
		}
		if dev.IdentityID != nil {
			return inputError("That MAC is a member of an identity; its DNS name comes from the identity (Settings → Identities).")
		}

		previous := dev.EffectiveHostname()
		dev.Hostname = hostname
		dev.OverrideHostname = &hostname
		dev.Excluded = false
		dev.Registered = true
		dev.RegisteredBy = actor
		if owner != nil {
			applyOwner(&dev, *owner)
		}
		// DNSRecordSynced is left alone on purpose: for a device that already
		// has a record, the next reconcile sees the hostname change and
		// updates that record in place instead of leaving the old name behind.
		if err := tx.Save(&dev).Error; err != nil {
			return err
		}

		detail := "registered before first connection as " + hostname
		if existed {
			detail = fmt.Sprintf("re-registered: hostname %s -> %s", previous, hostname)
		}
		if owner != nil {
			detail += ", owner " + owner.Username
		}
		writeAuditEvent(tx, h.Logger, mac, actor, db.SyncEventRegister, detail, true)
		return nil
	})
	if err != nil {
		if isUniqueViolation(err) {
			return db.Device{}, false, inputError("That MAC was just registered by someone else; try again.")
		}
		return db.Device{}, false, err
	}
	return dev, existed, nil
}

func isUniqueViolation(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "unique") || strings.Contains(s, "duplicate")
}

func applyOwner(dev *db.Device, u directory.User) {
	dev.OwnerUsername = u.Username
	dev.OwnerName = u.Name
	dev.OwnerEmail = u.Email
}

func clearOwner(dev *db.Device) { applyOwner(dev, directory.User{}) }

// lookupOwner resolves a username against the directory, so only real
// directory users can be assigned.
func (h *Handlers) lookupOwner(ctx context.Context, username string) (directory.User, error) {
	if h.Directory == nil {
		return directory.User{}, inputError("LDAP is not configured, so devices can't be assigned to users.")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	u, err := h.Directory.Lookup(ctx, username)
	switch {
	case errors.Is(err, directory.ErrNotFound):
		return directory.User{}, inputError(fmt.Sprintf("No user %q in the directory.", strings.TrimSpace(username)))
	case err != nil:
		h.Logger.Error("ldap lookup failed", "username", username, "error", err)
		return directory.User{}, inputError("Could not reach the directory: " + err.Error())
	}
	return u, nil
}

// userSearch backs the owner autocomplete: JSON [{username,name,email}].
func (h *Handlers) userSearch(w http.ResponseWriter, r *http.Request) {
	if h.Directory == nil {
		http.Error(w, "LDAP is not configured", http.StatusNotFound)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	users := []directory.User{}
	if len(q) >= 2 {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		found, err := h.Directory.Search(ctx, q)
		if err != nil {
			h.Logger.Error("ldap search failed", "error", err)
			http.Error(w, "directory search failed", http.StatusBadGateway)
			return
		}
		if found != nil {
			users = found
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(users)
}

// setOwner assigns (or, with a blank username, unassigns) one device.
func (h *Handlers) setOwner(w http.ResponseWriter, r *http.Request) {
	dev, ok := h.deviceByID(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	back := "/devices/" + strconv.FormatUint(uint64(dev.ID), 10)
	flash, err := h.assignOwner(r, dev, r.FormValue("owner"))
	if err != nil {
		var ie inputError
		if !errors.As(err, &ie) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		flash = ie.Error()
	}
	http.Redirect(w, r, back+"?flash="+url.QueryEscape(flash), http.StatusSeeOther)
}

// assignOwner sets dev's owner to username (blank clears it) and records
// the audit event, returning a message for the admin.
func (h *Handlers) assignOwner(r *http.Request, dev db.Device, username string) (string, error) {
	if strings.TrimSpace(username) == "" {
		if dev.OwnerUsername == "" {
			return "No owner was set.", nil
		}
		previous := dev.OwnerUsername
		clearOwner(&dev)
		if err := h.DB.Save(&dev).Error; err != nil {
			return "", err
		}
		h.logAudit(r, dev.MAC, db.SyncEventAssign, "unassigned from "+previous, true)
		return "Unassigned " + dev.MAC + ".", nil
	}
	u, err := h.lookupOwner(r.Context(), username)
	if err != nil {
		return "", err
	}
	applyOwner(&dev, u)
	if err := h.DB.Save(&dev).Error; err != nil {
		return "", err
	}
	h.logAudit(r, dev.MAC, db.SyncEventAssign, "assigned to "+u.Username, true)
	return fmt.Sprintf("Assigned %s to %s.", dev.MAC, u.Username), nil
}
