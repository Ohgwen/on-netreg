package unifi

// NetworkClient is a normalized view of a client reported by the UniFi
// Network API, merged from the active-client and known-client endpoints.
type NetworkClient struct {
	MAC string
	// Name is the user-set alias for this client in the UniFi UI, if any.
	Name string
	// Hostname is the DHCP-reported hostname, if any.
	Hostname string
	IP       string
	IsWired  bool
	IsGuest  bool
	// Online reflects whether the client was present in the active-client
	// list (/stat/sta) this cycle, as opposed to only /stat/alluser.
	Online bool
}

// apiClient is the raw shape of one entry from /stat/sta or /stat/alluser.
// Only the fields netreg cares about are modeled; the rest are ignored.
type apiClient struct {
	MAC         string `json:"mac"`
	Name        string `json:"name"`
	Hostname    string `json:"hostname"`
	IP          string `json:"ip"`
	FixedIP     string `json:"fixed_ip"`
	UseFixedIP  bool   `json:"use_fixedip"`
	IsWired     bool   `json:"is_wired"`
	IsGuest     bool   `json:"is_guest"`
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
