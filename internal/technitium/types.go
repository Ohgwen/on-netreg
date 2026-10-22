package technitium

// AddRecordRequest creates a new DNS record. See
// https://github.com/TechnitiumSoftware/DnsServer/blob/master/APIDOCS.md
// (POST /api/zones/records/add).
type AddRecordRequest struct {
	Domain    string
	Zone      string
	Type      string // e.g. "A"
	TTL       int
	IPAddress string
	// CNAME is the target FQDN when Type is "CNAME".
	CNAME         string
	Overwrite     bool
	PTR           bool
	CreatePTRZone bool
	// PTRName is the target name of a PTR record (Type "PTR").
	PTRName string
	// Text is the value of a TXT record.
	Text     string
	Comments string
}

// UpdateRecordRequest updates an existing record, identified by
// Domain/Type/IPAddress, to the New* values.
// NOTE: the exact parameter names for /api/zones/records/update were not
// fully confirmed from documentation during development (see README) --
// verify against your Technitium server's API docs before relying on
// hostname/IP changes being applied correctly.
type UpdateRecordRequest struct {
	Domain       string
	Zone         string
	Type         string
	IPAddress    string // current value, to identify the record
	NewDomain    string
	NewIPAddress string
	NewTTL       int
	// PTR asks Technitium to move the A/AAAA record's reverse PTR record
	// along with it; without it a rename leaves the old name in reverse DNS.
	PTR           bool
	CreatePTRZone bool
	Comments      string
}

// DeleteRecordRequest removes a record. See
// (POST /api/zones/records/delete).
type DeleteRecordRequest struct {
	Domain    string
	Zone      string
	Type      string
	IPAddress string
	// PTRName and Text identify which PTR or TXT record at Domain to
	// remove, since a name can hold several. A CNAME needs no value: a name
	// holds at most one.
	PTRName string
	Text    string
}

// DNSRecord is a record as returned by /api/zones/records/get.
type DNSRecord struct {
	Name     string         `json:"name"`
	Type     string         `json:"type"`
	TTL      int            `json:"ttl"`
	RData    map[string]any `json:"rData"`
	Disabled bool           `json:"disabled"`
	Comments string         `json:"comments"`
	// LastUsedOn and LastModified are timestamps as Technitium formats
	// them; LastModified is only reported by newer server versions, and
	// either is "0001-01-01T00:00:00" when it never happened.
	LastUsedOn   string `json:"lastUsedOn"`
	LastModified string `json:"lastModified"`
}

type loginResponse struct {
	Status       string `json:"status"`
	ErrorMessage string `json:"errorMessage"`
	Token        string `json:"token"`
}

type apiResult struct {
	Status       string `json:"status"`
	ErrorMessage string `json:"errorMessage"`
}

type getRecordsResponse struct {
	apiResult
	Response struct {
		Records []DNSRecord `json:"records"`
	} `json:"response"`
}

// ZoneInfo describes one zone hosted on the Technitium server, as returned
// by /api/zones/list.
type ZoneInfo struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Disabled bool   `json:"disabled"`
}

type listZonesResponse struct {
	apiResult
	Response struct {
		Zones []ZoneInfo `json:"zones"`
	} `json:"response"`
}
