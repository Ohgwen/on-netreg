package technitium

// AddRecordRequest creates a new DNS record. See
// https://github.com/TechnitiumSoftware/DnsServer/blob/master/APIDOCS.md
// (POST /api/zones/records/add).
type AddRecordRequest struct {
	Domain        string
	Zone          string
	Type          string // e.g. "A"
	TTL           int
	IPAddress     string
	Overwrite     bool
	PTR           bool
	CreatePTRZone bool
	Comments      string
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
	Comments     string
}

// DeleteRecordRequest removes a record. See
// (POST /api/zones/records/delete).
type DeleteRecordRequest struct {
	Domain    string
	Zone      string
	Type      string
	IPAddress string
}

// DNSRecord is a record as returned by /api/zones/records/get.
type DNSRecord struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	TTL      int    `json:"ttl"`
	RData    map[string]any `json:"rData"`
	Disabled bool   `json:"disabled"`
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
