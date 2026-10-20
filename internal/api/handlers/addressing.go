package handlers

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"gorm.io/gorm"

	"github.com/Ohgwen/on-netreg/internal/db"
)

// setAddressing changes how a device is addressed. Reserved and DHCP are
// written straight through to the device's UniFi controller (set or clear
// its fixed IP); Static clears any UniFi reservation and pins the device's
// DNS record to the address entered here. The DNS record itself follows on
// the next sync cycle, like a hostname override.
func (h *Handlers) setAddressing(w http.ResponseWriter, r *http.Request) {
	dev, ok := h.deviceByID(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	back := func(flash string) {
		http.Redirect(w, r, "/devices/"+strconv.FormatUint(uint64(dev.ID), 10)+"?flash="+url.QueryEscape(flash), http.StatusSeeOther)
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	_, flash, err := h.changeAddressing(ctx, r, dev, db.AddressMode(r.FormValue("mode")), r.FormValue("ip"))
	if err != nil {
		flash = err.Error()
	}
	back(flash)
}

// changeAddressing validates and applies a new addressing mode to dev:
// the UniFi reservation first (so a controller failure leaves netreg
// unchanged), then the device row and an audit event. It returns the saved
// device and a message for the admin; validation and UniFi failures are
// inputErrors, safe to show on a form.
func (h *Handlers) changeAddressing(ctx context.Context, r *http.Request, dev db.Device, mode db.AddressMode, ip string) (db.Device, string, error) {
	if mode != db.AddressDHCP {
		if err := h.validateAssignedIP(dev, mode, ip); err != nil {
			return dev, "", inputError(err.Error())
		}
	}

	if err := h.applyReservation(ctx, dev, mode, ip); err != nil {
		h.Logger.Error("failed to update unifi reservation", "mac", dev.MAC, "mode", mode, "error", err)
		h.logAudit(r, dev.MAC, db.SyncEventAddressing, fmt.Sprintf("set addressing to %s %s: %v", mode.Label(), ip, err), false)
		return dev, "", inputError("UniFi update failed: " + err.Error())
	}

	dev.AddressMode = mode
	dev.AssignedIP = ""
	if mode != db.AddressDHCP {
		dev.AssignedIP = ip
	}
	dev.IsFixedIP = mode == db.AddressReserved
	if err := h.DB.Save(&dev).Error; err != nil {
		return dev, "", err
	}

	detail := "set addressing to " + mode.Label()
	if dev.AssignedIP != "" {
		detail += " " + dev.AssignedIP
	}
	h.logAudit(r, dev.MAC, db.SyncEventAddressing, detail, true)

	if mode == db.AddressReserved {
		return dev, "Reserved in UniFi. The device picks up the new address when it next renews its lease (reconnect it to apply now).", nil
	}
	return dev, "Saved. DNS updates on the next sync.", nil
}

// pushName sets the device's client alias on its UniFi controller to its
// current netreg hostname, so the UniFi UI shows the same name as DNS. The
// sync engine reads that alias back as the device's UniFi name, so the two
// stay in agreement afterwards.
func (h *Handlers) pushName(w http.ResponseWriter, r *http.Request) {
	dev, ok := h.deviceByID(w, r)
	if !ok {
		return
	}
	back := func(flash string) {
		http.Redirect(w, r, "/devices/"+strconv.FormatUint(uint64(dev.ID), 10)+"?flash="+url.QueryEscape(flash), http.StatusSeeOther)
	}

	name := dev.EffectiveHostname()
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	client, err := h.unifiWriter(ctx, dev)
	if err == nil {
		err = client.SetName(ctx, dev.MAC, name)
	}
	if err != nil {
		h.Logger.Error("failed to push name to unifi", "mac", dev.MAC, "name", name, "error", err)
		h.logAudit(r, dev.MAC, db.SyncEventPushName, fmt.Sprintf("push name %q to UniFi: %v", name, err), false)
		back("UniFi update failed: " + err.Error())
		return
	}

	dev.UniFiName = name
	if err := h.DB.Save(&dev).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.logAudit(r, dev.MAC, db.SyncEventPushName, fmt.Sprintf("set UniFi name to %q", name), true)
	back(fmt.Sprintf("UniFi name set to %q.", name))
}

func (h *Handlers) unifiWriter(ctx context.Context, dev db.Device) (UnifiWriter, error) {
	if h.Unifi == nil {
		return nil, errors.New("no UniFi connection configured")
	}
	if dev.ControllerID == 0 {
		return nil, errors.New("this device hasn't been seen by a UniFi console yet; connect it once, then try again")
	}
	return h.Unifi(ctx, dev.ControllerID)
}

// applyReservation brings the device's UniFi fixed-IP setting in line with
// mode. Only Reserved needs a reservation; DHCP and Static clear one if
// UniFi currently has it.
func (h *Handlers) applyReservation(ctx context.Context, dev db.Device, mode db.AddressMode, ip string) error {
	if mode != db.AddressReserved && !dev.IsFixedIP {
		return nil
	}
	client, err := h.unifiWriter(ctx, dev)
	if err != nil {
		return err
	}
	if mode != db.AddressReserved {
		return client.ClearFixedIP(ctx, dev.MAC)
	}

	network, err := h.deviceNetwork(dev)
	if err != nil {
		return err
	}
	return client.SetFixedIP(ctx, dev.MAC, ip, network.UnifiNetworkID)
}

// validateAssignedIP checks that ip is usable as a reserved or static
// address for dev: a valid IPv4 address, inside the device's network subnet
// when that's known, and not already claimed by another device.
func (h *Handlers) validateAssignedIP(dev db.Device, mode db.AddressMode, ip string) error {
	if mode != db.AddressReserved && mode != db.AddressStatic {
		return fmt.Errorf("unknown addressing mode %q", mode)
	}
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() == nil {
		return fmt.Errorf("%q is not a valid IPv4 address", ip)
	}

	network, err := h.deviceNetwork(dev)
	switch {
	case err == nil && network.IPSubnet != "":
		if _, subnet, perr := net.ParseCIDR(network.IPSubnet); perr == nil && !subnet.Contains(parsed) {
			return fmt.Errorf("%s is outside %s's subnet %s", ip, network.Name, subnet)
		}
	case err != nil && mode == db.AddressReserved:
		// UniFi reservations are per network, so one can't be made
		// without knowing which network the device is on.
		return err
	}

	var other db.Device
	err = h.DB.Where("id <> ? AND (ip_address = ? OR assigned_ip = ?)", dev.ID, ip, ip).First(&other).Error
	if err == nil {
		return fmt.Errorf("%s is already in use by %s (%s)", ip, other.EffectiveHostname(), other.MAC)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return nil
}

func (h *Handlers) deviceNetwork(dev db.Device) (db.UnifiNetwork, error) {
	if dev.NetworkID == nil {
		return db.UnifiNetwork{}, errors.New("this device's network isn't mapped yet; refresh networks under Settings > Controllers")
	}
	var network db.UnifiNetwork
	if err := h.DB.First(&network, *dev.NetworkID).Error; err != nil {
		return db.UnifiNetwork{}, fmt.Errorf("loading device network: %w", err)
	}
	return network, nil
}
