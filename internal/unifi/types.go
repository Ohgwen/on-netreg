package unifi

// NetworkClient is a normalized view of a client reported by the UniFi
// Network API, merged from the active-client and known-client endpoints.
type NetworkClient struct {
	MAC string
	// Name is the user-set alias for this client in the UniFi UI, if any.
	Name string
	// Hostname is the DHCP-reported hostname, if any.
	Hostname   string
	IP         string
	IsWired    bool
	IsGuest    bool
	IsFixedIP  bool
	// NetworkID is the controller's network/VLAN id (_id) this client is
	// on, if reported. Used to resolve which DNS zone it syncs to.
	NetworkID string
	// Network is the network/VLAN's display name, if reported.
	Network string
	VLAN    int
	// Online reflects whether the client was present in the active-client
	// list (/stat/sta) this cycle, as opposed to only /stat/alluser.
	Online bool
}

// apiClient is the raw shape of one entry from /stat/sta or /stat/alluser.
// Only the fields netreg cares about are modeled; the rest are ignored.
type apiClient struct {
	MAC        string `json:"mac"`
	Name       string `json:"name"`
	Hostname   string `json:"hostname"`
	IP         string `json:"ip"`
	FixedIP    string `json:"fixed_ip"`
	UseFixedIP bool   `json:"use_fixedip"`
	IsWired    bool   `json:"is_wired"`
	IsGuest    bool   `json:"is_guest"`
	NetworkID  string `json:"network_id"`
	Network    string `json:"network"`
	VLAN       int    `json:"vlan"`
}

// Network is a normalized view of a network/VLAN configured on a UniFi
// controller, as returned by GET /rest/networkconf.
type Network struct {
	ID       string
	Name     string
	VLAN     int
	IPSubnet string
	// DHCPLeaseTimeSeconds is the network's configured DHCP lease time, if
	// DHCP is enabled on it. Used to estimate a client's lease expiry.
	DHCPLeaseTimeSeconds int
}

// apiNetwork is the raw shape of one entry from /rest/networkconf.
// NOTE: field availability/naming has not been confirmed against a live
// controller across firmware versions -- verify before relying on it (see
// the similar disclaimer on technitium.UpdateRecordRequest).
type apiNetwork struct {
	ID             string `json:"_id"`
	Name           string `json:"name"`
	VLAN           int    `json:"vlan"`
	IPSubnet       string `json:"ip_subnet"`
	DHCPDEnabled   bool   `json:"dhcpd_enabled"`
	DHCPLeaseTime  int    `json:"dhcpd_leasetime"`
}

type networkConfResponse struct {
	Meta struct {
		RC  string `json:"rc"`
		Msg string `json:"msg"`
	} `json:"meta"`
	Data []apiNetwork `json:"data"`
}

type apiResponse struct {
	Meta struct {
		RC  string `json:"rc"`
		Msg string `json:"msg"`
	} `json:"meta"`
	Data []apiClient `json:"data"`
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Remember bool   `json:"remember"`
	Token    string `json:"token,omitempty"`
}

type loginMeta struct {
	Meta struct {
		RC  string `json:"rc"`
		Msg string `json:"msg"`
	} `json:"meta"`
}
