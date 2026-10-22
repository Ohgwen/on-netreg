package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/Ohgwen/on-netreg/internal/db"
	"github.com/Ohgwen/on-netreg/internal/settings"
	"github.com/Ohgwen/on-netreg/internal/technitium"
)

// networkView is a network with what the networks pages show alongside it.
type networkView struct {
	db.UnifiNetwork
	// ControllerName is the console an imported network came from.
	ControllerName string
	DeviceCount    int64
	// EffectiveZone is the zone the network's devices sync to: its own
	// zone, else the fallback named by ZoneSource.
	EffectiveZone string
	ZoneSource    string
}

// networkRecord is one DNS record shown on a network's page.
type networkRecord struct {
	technitium.DNSRecord
	// Zone is the zone the record lives in, passed back on delete; blank
	// for a reverse record (the server finds its reverse zone).
	Zone string
	// DeviceID/DeviceName link the record to the netreg device at its
	// address, if any.
	DeviceID   uint
	DeviceName string
	// ManagedBy names what in netreg publishes this exact record (a device,
	// an alias, an identity). Such records are kept in step by the sync
	// engine, so they are changed from there rather than deleted here.
	ManagedBy string
}

// networkPage is everything settings_network.html renders.
type networkPage struct {
	Network networkView
	Devices []db.Device
	// Zone is the forward zone whose records are listed.
	Zone    string
	Forward []networkRecord
	Reverse []networkRecord
	// ShowAll lists every record in Zone rather than only those in the
	// network's subnet.
	ShowAll      bool
	RecordsError string
	ReverseError string
	ZoneNames    []string
}

// recordTypes are the record types the network page can add.
var recordTypes = []string{"A", "AAAA", "CNAME", "TXT", "PTR"}

func (h *SettingsHandlers) networkRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /settings/networks", h.networksPage)
	mux.HandleFunc("POST /settings/networks", h.addNetwork)
	mux.HandleFunc("GET /settings/networks/{id}", h.networkDetail)
	mux.HandleFunc("POST /settings/networks/{id}", h.editNetwork)
	mux.HandleFunc("POST /settings/networks/{id}/delete", h.deleteNetwork)
	mux.HandleFunc("POST /settings/networks/{id}/records", h.addNetworkRecord)
	mux.HandleFunc("POST /settings/networks/{id}/records/delete", h.deleteNetworkRecord)
}

func (h *SettingsHandlers) kick() {
	if h.Engine != nil {
		h.Engine.Kick()
	}
}

func networkURL(n db.UnifiNetwork) string {
	return "/settings/networks/" + strconv.FormatUint(uint64(n.ID), 10)
}

func (h *SettingsHandlers) networkByID(w http.ResponseWriter, r *http.Request) (db.UnifiNetwork, bool) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid network id", http.StatusBadRequest)
		return db.UnifiNetwork{}, false
	}
	var n db.UnifiNetwork
	if err := h.DB.First(&n, uint(id)).Error; err != nil {
		http.Error(w, "network not found", http.StatusNotFound)
		return db.UnifiNetwork{}, false
	}
	return n, true
}

// networkViews decorates networks with their controller, device count and
// effective zone.
func (h *SettingsHandlers) networkViews(networks []db.UnifiNetwork) ([]networkView, error) {
	var controllers []db.UnifiController
	if err := h.DB.Find(&controllers).Error; err != nil {
		return nil, err
	}
	byID := make(map[uint]db.UnifiController, len(controllers))
	for _, c := range controllers {
		byID[c.ID] = c
	}
	appSettings, err := settings.LoadApp(h.DB)
	if err != nil {
		return nil, err
	}

	var counts []struct {
		NetworkID uint
		N         int64
	}
	if err := h.DB.Model(&db.Device{}).Select("network_id, count(*) as n").Where("network_id IS NOT NULL").Group("network_id").Scan(&counts).Error; err != nil {
		return nil, err
	}
	countByID := make(map[uint]int64, len(counts))
	for _, c := range counts {
		countByID[c.NetworkID] = c.N
	}

	views := make([]networkView, 0, len(networks))
	for _, n := range networks {
		v := networkView{UnifiNetwork: n, DeviceCount: countByID[n.ID]}
		ctrl, imported := byID[n.ControllerID]
		if imported {
			v.ControllerName = ctrl.Name
		}
		switch {
		case n.Zone != "":
			v.EffectiveZone, v.ZoneSource = n.Zone, "network"
		case imported && ctrl.DefaultZone != "":
			v.EffectiveZone, v.ZoneSource = ctrl.DefaultZone, "controller default"
		default:
			// A manual network's devices fall back to the default zone of
			// the console that sees them; the global default is the best
			// single answer to show.
			v.EffectiveZone, v.ZoneSource = appSettings.DefaultZone, "default"
		}
		views = append(views, v)
	}
	return views, nil
}

// ---- Network list / create ----

func (h *SettingsHandlers) networksPage(w http.ResponseWriter, r *http.Request) {
	var networks []db.UnifiNetwork
	if err := h.DB.Order("manual desc, controller_id, name").Find(&networks).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	views, err := h.networkViews(networks)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.render(w, r, "settings_networks", pageData{
		Title:        "Settings · Networks",
		NetworkViews: views,
		ZoneNames:    h.liveZoneNames(r.Context()),
		Flash:        r.URL.Query().Get("flash"),
	})
}

// networkForm is a manual network's editable fields, validated.
type networkForm struct {
	Name, Subnet, Zone string
	VLAN, LeaseSeconds int
}

func (h *SettingsHandlers) parseNetworkForm(r *http.Request, selfID uint) (networkForm, error) {
	f := networkForm{
		Name:   strings.TrimSpace(r.FormValue("name")),
		Subnet: strings.TrimSpace(r.FormValue("subnet")),
		Zone:   strings.TrimSuffix(strings.ToLower(strings.TrimSpace(r.FormValue("zone"))), "."),
	}
	if f.Name == "" {
		return f, inputError("Enter a name for the network.")
	}
	ip, subnet, err := net.ParseCIDR(f.Subnet)
	if err != nil || ip.To4() == nil {
		return f, inputError(fmt.Sprintf("%q is not an IPv4 subnet; use CIDR notation such as 10.0.20.0/24.", f.Subnet))
	}
	if f.Zone != "" && !zoneNameRe.MatchString(f.Zone) {
		return f, inputError("Not a valid zone name.")
	}
	if v := strings.TrimSpace(r.FormValue("vlan")); v != "" {
		if f.VLAN, err = strconv.Atoi(v); err != nil || f.VLAN < 0 || f.VLAN > 4094 {
			return f, inputError("VLAN must be a number from 0 to 4094.")
		}
	}
	if v := strings.TrimSpace(r.FormValue("lease_hours")); v != "" {
		hours, err := strconv.ParseFloat(v, 64)
		if err != nil || hours < 0 {
			return f, inputError("DHCP lease time must be a number of hours.")
		}
		f.LeaseSeconds = int(hours * 3600)
	}

	// The same range defined twice by hand would make which one a device
	// lands on arbitrary.
	var manual []db.UnifiNetwork
	if err := h.DB.Where("manual = ? AND id <> ?", true, selfID).Find(&manual).Error; err != nil {
		return f, err
	}
	for _, m := range manual {
		if _, other, err := net.ParseCIDR(m.IPSubnet); err == nil && other.String() == subnet.String() {
			return f, inputError(fmt.Sprintf("%s is already defined as %q.", subnet, m.Name))
		}
	}
	return f, nil
}

func (h *SettingsHandlers) addNetwork(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f, err := h.parseNetworkForm(r, 0)
	if err != nil {
		h.formFailed(w, r, "/settings/networks", err)
		return
	}
	n := db.UnifiNetwork{
		UnifiNetworkID:       manualNetworkID(),
		Manual:               true,
		Name:                 f.Name,
		IPSubnet:             f.Subnet,
		VLAN:                 f.VLAN,
		DHCPLeaseTimeSeconds: f.LeaseSeconds,
		Zone:                 f.Zone,
	}
	if err := h.DB.Create(&n).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.logAudit(r, db.SyncEventNetwork, fmt.Sprintf("added network %q (%s)", n.Name, n.IPSubnet))
	h.kick()
	http.Redirect(w, r, networkURL(n)+"?flash="+url.QueryEscape("Network added. Devices in "+n.IPSubnet+" move onto it within a few seconds."), http.StatusSeeOther)
}

// manualNetworkID makes the placeholder UniFi id a manual network needs to
// be unique in the (controller, UniFi id) index.
func manualNetworkID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "manual:" + hex.EncodeToString(b)
}

// formFailed shows an input error back on the page the form came from, or
// fails the request for anything else.
func (h *SettingsHandlers) formFailed(w http.ResponseWriter, r *http.Request, back string, err error) {
	var ie inputError
	if !errors.As(err, &ie) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, back+"?flash="+url.QueryEscape(ie.Error()), http.StatusSeeOther)
}

// ---- Network detail / edit / delete ----

// editNetwork saves a network. A manual network's every field is editable;
// an imported one only has its zone set here, since the rest is refreshed
// from its controller.
func (h *SettingsHandlers) editNetwork(w http.ResponseWriter, r *http.Request) {
	n, ok := h.networkByID(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !n.Manual {
		zone := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(r.FormValue("zone"))), ".")
		if zone != "" && !zoneNameRe.MatchString(zone) {
			h.formFailed(w, r, networkURL(n), inputError("Not a valid zone name."))
			return
		}
		n.Zone = zone
	} else {
		f, err := h.parseNetworkForm(r, n.ID)
		if err != nil {
			h.formFailed(w, r, networkURL(n), err)
			return
		}
		n.Name, n.IPSubnet, n.VLAN, n.DHCPLeaseTimeSeconds, n.Zone = f.Name, f.Subnet, f.VLAN, f.LeaseSeconds, f.Zone
	}
	if err := h.DB.Save(&n).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.logAudit(r, db.SyncEventNetwork, fmt.Sprintf("updated network %q (%s, zone %s)", n.Name, n.IPSubnet, zoneLabel(n.Zone)))
	h.kick()
	http.Redirect(w, r, networkURL(n)+"?flash="+url.QueryEscape("Network saved. DNS follows within a few seconds."), http.StatusSeeOther)
}

// deleteNetwork removes a manual network. Its devices are re-placed by
// subnet on the next sync cycle (onto an imported network covering the same
// range, if there is one). Imported networks can't be deleted: the next
// import would only bring them back.
func (h *SettingsHandlers) deleteNetwork(w http.ResponseWriter, r *http.Request) {
	n, ok := h.networkByID(w, r)
	if !ok {
		return
	}
	if !n.Manual {
		h.formFailed(w, r, networkURL(n), inputError("Networks imported from UniFi can't be deleted here; remove them on the controller."))
		return
	}
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&db.Device{}).Where("network_id = ?", n.ID).Update("network_id", nil).Error; err != nil {
			return err
		}
		return tx.Delete(&n).Error
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.logAudit(r, db.SyncEventNetwork, fmt.Sprintf("deleted network %q (%s)", n.Name, n.IPSubnet))
	h.kick()
	http.Redirect(w, r, "/settings/networks?flash="+url.QueryEscape(fmt.Sprintf("Deleted %q.", n.Name)), http.StatusSeeOther)
}

func (h *SettingsHandlers) networkDetail(w http.ResponseWriter, r *http.Request) {
	n, ok := h.networkByID(w, r)
	if !ok {
		return
	}
	views, err := h.networkViews([]db.UnifiNetwork{n})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	page := networkPage{Network: views[0], ShowAll: r.URL.Query().Get("all") == "1"}
	if err := h.DB.Where("network_id = ?", n.ID).Order("hostname").Find(&page.Devices).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	page.Zone = page.Network.EffectiveZone
	h.loadNetworkRecords(r.Context(), &page)
	page.ZoneNames = h.liveZoneNames(r.Context())

	h.render(w, r, "settings_network", pageData{
		Title:       "Settings · Network " + n.Name,
		NetworkPage: page,
		Flash:       r.URL.Query().Get("flash"),
	})
}

// recordOwners indexes everything netreg publishes, to tell which records
// on a network page it manages and which device sits at an address.
type recordOwners struct {
	// managed maps "type name value" (lowercased, no trailing dots) to who
	// publishes that record.
	managed map[string]string
	// byIP maps an address to the device holding it.
	byIP map[string]db.Device
}

func recordKey(rtype, name, value string) string {
	norm := func(s string) string { return strings.TrimSuffix(strings.ToLower(s), ".") }
	return strings.ToUpper(rtype) + " " + norm(name) + " " + norm(value)
}

func (h *SettingsHandlers) loadRecordOwners() (recordOwners, error) {
	o := recordOwners{managed: map[string]string{}, byIP: map[string]db.Device{}}
	var devices []db.Device
	if err := h.DB.Find(&devices).Error; err != nil {
		return o, err
	}
	for _, d := range devices {
		if d.IPAddress != "" {
			o.byIP[d.IPAddress] = d
		}
		if d.DNSRecordSynced && d.Zone != "" && d.IPAddress != "" {
			o.managed[recordKey("A", d.Hostname+"."+d.Zone, d.IPAddress)] = "device " + d.EffectiveHostname()
		}
	}
	var aliases []db.DeviceAlias
	if err := h.DB.Where("synced = ?", true).Find(&aliases).Error; err != nil {
		return o, err
	}
	for _, a := range aliases {
		o.managed[recordKey("CNAME", a.SyncedName, a.SyncedTarget)] = "alias"
	}
	var identities []db.Identity
	if err := h.DB.Where("dns_record_synced = ?", true).Find(&identities).Error; err != nil {
		return o, err
	}
	for _, i := range identities {
		o.managed[recordKey("A", i.SyncedHostname+"."+i.Zone, i.IPAddress)] = "identity " + i.EffectiveHostname()
	}
	return o, nil
}

// loadNetworkRecords fills page's forward records (from its zone) and
// reverse records (PTRs for addresses in its subnet). Failures are shown on
// the page rather than failing it.
func (h *SettingsHandlers) loadNetworkRecords(ctx context.Context, page *networkPage) {
	client, err := h.technitiumClient()
	if err != nil {
		page.RecordsError = "Technitium is not configured: " + err.Error()
		return
	}
	owners, err := h.loadRecordOwners()
	if err != nil {
		page.RecordsError = err.Error()
		return
	}
	_, subnet, err := net.ParseCIDR(page.Network.IPSubnet)
	if err != nil {
		subnet = nil
	}
	inSubnet := func(ip string) bool {
		parsed := net.ParseIP(ip)
		return subnet != nil && parsed != nil && subnet.Contains(parsed)
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	annotate := func(rec technitium.DNSRecord, zone string) networkRecord {
		nr := networkRecord{DNSRecord: rec, Zone: zone, ManagedBy: owners.managed[recordKey(rec.Type, rec.Name, rec.Value())]}
		ip := rec.Value()
		if strings.EqualFold(rec.Type, "PTR") {
			if parsed, ok := technitium.ReverseIP(rec.Name); ok {
				ip = parsed.String()
			}
		}
		if d, ok := owners.byIP[ip]; ok {
			nr.DeviceID, nr.DeviceName = d.ID, d.EffectiveHostname()
		}
		return nr
	}

	if page.Zone == "" {
		page.RecordsError = "This network has no zone, so it has no forward records. Set a zone below."
	} else if records, err := client.ZoneRecords(ctx, page.Zone); err != nil {
		page.RecordsError = fmt.Sprintf("Couldn't list records in %s: %v", page.Zone, err)
	} else {
		// A name is "on this network" if any of its address records is in
		// the subnet; CNAMEs pointing at such a name come along.
		local := map[string]bool{}
		for _, rec := range records {
			if strings.EqualFold(rec.Type, "A") && inSubnet(rec.Value()) {
				local[strings.ToLower(rec.Name)] = true
			}
		}
		for _, rec := range records {
			show := page.ShowAll
			switch strings.ToUpper(rec.Type) {
			case "A":
				show = show || inSubnet(rec.Value())
			case "CNAME":
				show = show || local[strings.TrimSuffix(strings.ToLower(rec.Value()), ".")]
			}
			if show {
				page.Forward = append(page.Forward, annotate(rec, page.Zone))
			}
		}
		sortRecords(page.Forward)
	}

	if subnet == nil {
		return
	}
	reverse, err := reverseDomain(subnet)
	if err != nil {
		page.ReverseError = err.Error()
		return
	}
	records, err := client.GetRecords(ctx, reverse, "", true)
	if err != nil {
		page.ReverseError = fmt.Sprintf("No reverse zone covers %s (%v).", subnet, err)
		return
	}
	for _, rec := range records {
		if !strings.EqualFold(rec.Type, "PTR") {
			continue
		}
		if ip, ok := technitium.ReverseIP(rec.Name); ok && subnet.Contains(ip) {
			page.Reverse = append(page.Reverse, annotate(rec, ""))
		}
	}
	sort.Slice(page.Reverse, func(i, j int) bool {
		a, _ := technitium.ReverseIP(page.Reverse[i].Name)
		b, _ := technitium.ReverseIP(page.Reverse[j].Name)
		return compareIP(a, b) < 0
	})
}

func sortRecords(records []networkRecord) {
	sort.SliceStable(records, func(i, j int) bool {
		a, b := records[i], records[j]
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		if strings.EqualFold(a.Type, "A") {
			return compareIP(net.ParseIP(a.Value()), net.ParseIP(b.Value())) < 0
		}
		return a.Name < b.Name
	})
}

func compareIP(a, b net.IP) int {
	return strings.Compare(string(a.To16()), string(b.To16()))
}

// reverseDomain is the in-addr.arpa name covering subnet at octet
// granularity (a /20 is listed through its /16), used to find the reverse
// zone holding the subnet's PTRs.
func reverseDomain(subnet *net.IPNet) (string, error) {
	ip := subnet.IP.To4()
	if ip == nil {
		return "", errors.New("reverse records are only listed for IPv4 networks")
	}
	ones, _ := subnet.Mask.Size()
	octets := ones / 8
	if octets == 0 {
		return "", errors.New("subnet is too large to list reverse records for")
	}
	if octets > 3 {
		octets = 3
	}
	parts := make([]string, 0, octets+1)
	for i := octets - 1; i >= 0; i-- {
		parts = append(parts, strconv.Itoa(int(ip[i])))
	}
	return strings.Join(append(parts, "in-addr.arpa"), "."), nil
}

// ---- Records ----

func (h *SettingsHandlers) addNetworkRecord(w http.ResponseWriter, r *http.Request) {
	n, ok := h.networkByID(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	views, err := h.networkViews([]db.UnifiNetwork{n})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	req, err := buildRecordRequest(n, views[0].EffectiveZone, r)
	if err != nil {
		h.formFailed(w, r, networkURL(n), err)
		return
	}

	client, err := h.technitiumClient()
	if err == nil {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		err = client.AddRecord(ctx, req)
	}
	value := req.IPAddress + req.CNAME + req.PTRName + req.Text
	detail := fmt.Sprintf("added %s %s -> %s", req.Type, req.Domain, value)
	if err != nil {
		writeAuditEvent(h.DB, h.Logger, "", h.CurrentUser(r), db.SyncEventDNSRecord, detail+": "+err.Error(), false)
		h.formFailed(w, r, networkURL(n), inputError("DNS server refused the record: "+err.Error()))
		return
	}
	h.logAudit(r, db.SyncEventDNSRecord, detail)
	http.Redirect(w, r, networkURL(n)+"?flash="+url.QueryEscape(fmt.Sprintf("Added %s record %s.", req.Type, req.Domain)), http.StatusSeeOther)
}

// buildRecordRequest validates the add-record form for network n, whose
// forward zone is zone. Names are relative to the zone ("@" or blank for the
// zone apex) unless already inside it; a PTR's name is the IP address it
// reverses, which must be in the network's subnet, as must an A record's
// address.
func buildRecordRequest(n db.UnifiNetwork, zone string, r *http.Request) (technitium.AddRecordRequest, error) {
	rtype := strings.ToUpper(strings.TrimSpace(r.FormValue("type")))
	name := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(r.FormValue("name"))), ".")
	value := strings.TrimSpace(r.FormValue("value"))
	req := technitium.AddRecordRequest{Type: rtype, Zone: zone}
	if ttl := strings.TrimSpace(r.FormValue("ttl")); ttl != "" {
		t, err := strconv.Atoi(ttl)
		if err != nil || t < 0 {
			return req, inputError("TTL must be a whole number of seconds.")
		}
		req.TTL = t
	}
	if value == "" {
		return req, inputError("Enter the record's value.")
	}
	_, subnet, _ := net.ParseCIDR(n.IPSubnet)
	inSubnet := func(ip net.IP) bool { return subnet == nil || subnet.Contains(ip) }

	if rtype == "PTR" {
		ip := net.ParseIP(name)
		if ip == nil || ip.To4() == nil {
			return req, inputError("For a PTR record, enter the IPv4 address it reverses as the name.")
		}
		if !inSubnet(ip) {
			return req, inputError(fmt.Sprintf("%s is outside %s.", ip, n.IPSubnet))
		}
		reverse, err := technitium.ReverseName(ip.String())
		if err != nil {
			return req, inputError(err.Error())
		}
		req.Domain, req.Zone, req.PTRName = reverse, "", strings.TrimSuffix(value, ".")
		return req, nil
	}

	if zone == "" {
		return req, inputError("This network has no zone to add records to; set one first.")
	}
	switch {
	case name == "" || name == "@":
		req.Domain = zone
	case name == zone || strings.HasSuffix(name, "."+zone):
		req.Domain = name
	default:
		req.Domain = name + "." + zone
	}
	if label := strings.TrimSuffix(strings.TrimSuffix(req.Domain, zone), "."); label != "" && !zoneNameRe.MatchString(label) && !strings.HasPrefix(label, "_") {
		return req, inputError(fmt.Sprintf("%q is not a valid name.", name))
	}

	switch rtype {
	case "A":
		ip := net.ParseIP(value)
		if ip == nil || ip.To4() == nil {
			return req, inputError(fmt.Sprintf("%q is not an IPv4 address.", value))
		}
		if !inSubnet(ip) {
			return req, inputError(fmt.Sprintf("%s is outside %s.", ip, n.IPSubnet))
		}
		req.IPAddress = ip.String()
		if r.FormValue("ptr") == "on" {
			req.PTR, req.CreatePTRZone = true, true
		}
	case "AAAA":
		ip := net.ParseIP(value)
		if ip == nil || ip.To4() != nil {
			return req, inputError(fmt.Sprintf("%q is not an IPv6 address.", value))
		}
		req.IPAddress = ip.String()
		if r.FormValue("ptr") == "on" {
			req.PTR, req.CreatePTRZone = true, true
		}
	case "CNAME":
		if req.Domain == zone {
			return req, inputError("A zone's apex can't be a CNAME.")
		}
		req.CNAME = strings.TrimSuffix(strings.ToLower(value), ".")
	case "TXT":
		req.Text = value
	default:
		return req, inputError("Choose one of " + strings.Join(recordTypes, ", ") + ".")
	}
	return req, nil
}

func (h *SettingsHandlers) deleteNetworkRecord(w http.ResponseWriter, r *http.Request) {
	n, ok := h.networkByID(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	name, zone := r.FormValue("name"), r.FormValue("zone")
	rtype, value := strings.ToUpper(r.FormValue("type")), r.FormValue("value")
	back := networkURL(n)
	if r.FormValue("all") == "1" {
		back += "?all=1"
	}

	owners, err := h.loadRecordOwners()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if by := owners.managed[recordKey(rtype, name, value)]; by != "" {
		h.formFailed(w, r, networkURL(n), inputError(fmt.Sprintf("That record is published by %s; change or exclude it there instead, or the next sync would put it back.", by)))
		return
	}
	if !(technitium.DNSRecord{Type: rtype}).Deletable() {
		h.formFailed(w, r, networkURL(n), inputError(rtype+" records can't be deleted from here."))
		return
	}

	client, err := h.technitiumClient()
	if err == nil {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		err = client.DeleteRecord(ctx, technitium.NewDeleteRequest(name, zone, rtype, value))
	}
	detail := fmt.Sprintf("deleted %s %s -> %s", rtype, name, value)
	if err != nil {
		writeAuditEvent(h.DB, h.Logger, "", h.CurrentUser(r), db.SyncEventDNSRecord, detail+": "+err.Error(), false)
		h.formFailed(w, r, networkURL(n), inputError("DNS server refused the delete: "+err.Error()))
		return
	}
	h.logAudit(r, db.SyncEventDNSRecord, detail)
	sep := "?"
	if strings.Contains(back, "?") {
		sep = "&"
	}
	http.Redirect(w, r, back+sep+"flash="+url.QueryEscape(fmt.Sprintf("Deleted %s record %s.", rtype, name)), http.StatusSeeOther)
}
