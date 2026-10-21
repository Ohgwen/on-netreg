// Package technitium is a client for the Technitium DNS Server HTTP API,
// used to keep DNS A records in sync with the device registry.
package technitium

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/Ohgwen/on-netreg/internal/config"
)

type Client struct {
	baseURL  string
	username string
	password string

	httpClient *http.Client

	mu    sync.Mutex
	token string
}

func New(cfg config.TechnitiumConfig) *Client {
	return &Client{
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		username:   cfg.Username,
		password:   cfg.Password,
		httpClient: &http.Client{},
	}
}

func (c *Client) login(ctx context.Context) error {
	q := url.Values{
		"user": {c.username},
		"pass": {c.password},
	}

	body, _, err := c.rawRequest(ctx, "/api/user/login", q)
	if err != nil {
		return fmt.Errorf("technitium login: %w", err)
	}

	var resp loginResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("decoding technitium login response: %w", err)
	}
	if resp.Status != "ok" {
		return fmt.Errorf("technitium login failed: %s", resp.ErrorMessage)
	}

	c.mu.Lock()
	c.token = resp.Token
	c.mu.Unlock()
	return nil
}

// request performs an authenticated API call, logging in first if needed
// and retrying once after a fresh login if the token was rejected.
func (c *Client) request(ctx context.Context, path string, params url.Values) (apiResult, []byte, error) {
	c.mu.Lock()
	token := c.token
	c.mu.Unlock()
	if token == "" {
		if err := c.login(ctx); err != nil {
			return apiResult{}, nil, err
		}
	}

	result, body, err := c.doAuthed(ctx, path, params)
	if err != nil {
		return apiResult{}, nil, err
	}
	if result.Status == "invalid-token" || (result.Status == "error" && strings.Contains(strings.ToLower(result.ErrorMessage), "token")) {
		if err := c.login(ctx); err != nil {
			return apiResult{}, nil, err
		}
		result, body, err = c.doAuthed(ctx, path, params)
		if err != nil {
			return apiResult{}, nil, err
		}
	}
	if result.Status != "ok" {
		return result, body, fmt.Errorf("technitium API error from %s: %s", path, result.ErrorMessage)
	}
	return result, body, nil
}

func (c *Client) doAuthed(ctx context.Context, path string, params url.Values) (apiResult, []byte, error) {
	c.mu.Lock()
	token := c.token
	c.mu.Unlock()

	q := cloneValues(params)
	q.Set("token", token)

	body, status, err := c.rawRequest(ctx, path, q)
	if err != nil {
		return apiResult{}, nil, err
	}
	if status < 200 || status >= 300 {
		return apiResult{}, body, fmt.Errorf("technitium API request to %s failed: status %d: %s", path, status, string(body))
	}

	var result apiResult
	if err := json.Unmarshal(body, &result); err != nil {
		return apiResult{}, nil, fmt.Errorf("decoding response from %s: %w", path, err)
	}
	return result, body, nil
}

func (c *Client) rawRequest(ctx context.Context, path string, params url.Values) ([]byte, int, error) {
	reqURL := c.baseURL + path + "?" + params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("building request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("reading response body: %w", err)
	}
	return body, resp.StatusCode, nil
}

func (c *Client) AddRecord(ctx context.Context, r AddRecordRequest) error {
	q := url.Values{
		"domain": {r.Domain},
		"type":   {r.Type},
	}
	// Blank zone lets Technitium pick the closest authoritative zone, which
	// is how PTR records find their reverse zone.
	if r.Zone != "" {
		q.Set("zone", r.Zone)
	}
	if r.TTL > 0 {
		q.Set("ttl", strconv.Itoa(r.TTL))
	}
	if r.IPAddress != "" {
		q.Set("ipAddress", r.IPAddress)
	}
	if r.CNAME != "" {
		q.Set("cname", r.CNAME)
	}
	if r.PTRName != "" {
		q.Set("ptrName", r.PTRName)
	}
	if r.Overwrite {
		q.Set("overwrite", "true")
	}
	if r.PTR {
		q.Set("ptr", "true")
	}
	if r.CreatePTRZone {
		q.Set("createPtrZone", "true")
	}
	if r.Comments != "" {
		q.Set("comments", r.Comments)
	}

	_, _, err := c.request(ctx, "/api/zones/records/add", q)
	return err
}

// UpdateRecord updates an existing record. If r.IPAddress is empty for an
// address record (the registry lost track of the device's last IP), the
// record's current value is looked up first, since Technitium requires it to
// identify which A/AAAA record to update. If that lookup doesn't find exactly
// one record, any records at the old name are replaced with a fresh one.
func (c *Client) UpdateRecord(ctx context.Context, r UpdateRecordRequest) error {
	if r.IPAddress == "" && isAddressType(r.Type) {
		current, err := c.currentAddresses(ctx, r.Domain, r.Zone, r.Type)
		if err != nil {
			return err
		}
		if len(current) != 1 {
			return c.replaceRecord(ctx, r, current)
		}
		r.IPAddress = current[0]
	}

	q := url.Values{
		"domain": {r.Domain},
		"zone":   {r.Zone},
		"type":   {r.Type},
	}
	if r.IPAddress != "" {
		q.Set("ipAddress", r.IPAddress)
	}
	if r.NewDomain != "" {
		q.Set("newDomain", r.NewDomain)
	}
	if r.NewIPAddress != "" {
		q.Set("newIpAddress", r.NewIPAddress)
	}
	if r.NewTTL > 0 {
		q.Set("newTtl", strconv.Itoa(r.NewTTL))
	}
	if r.PTR {
		q.Set("ptr", "true")
	}
	if r.CreatePTRZone {
		q.Set("createPtrZone", "true")
	}
	if r.Comments != "" {
		q.Set("comments", r.Comments)
	}

	_, _, err := c.request(ctx, "/api/zones/records/update", q)
	return err
}

// DeleteRecord removes a record. If r.IPAddress is empty for an address
// record, every A/AAAA record of that type at r.Domain is looked up and
// deleted, since Technitium requires the value to identify which one to
// remove. Finding none is not an error: the record is already gone.
func (c *Client) DeleteRecord(ctx context.Context, r DeleteRecordRequest) error {
	if r.IPAddress == "" && isAddressType(r.Type) {
		current, err := c.currentAddresses(ctx, r.Domain, r.Zone, r.Type)
		if err != nil {
			return err
		}
		for _, ip := range current {
			r.IPAddress = ip
			if err := c.DeleteRecord(ctx, r); err != nil {
				return err
			}
		}
		return nil
	}

	q := url.Values{
		"domain": {r.Domain},
		"zone":   {r.Zone},
		"type":   {r.Type},
	}
	if r.IPAddress != "" {
		q.Set("ipAddress", r.IPAddress)
	}

	_, _, err := c.request(ctx, "/api/zones/records/delete", q)
	return err
}

func (c *Client) GetRecords(ctx context.Context, domain, zone string, listZone bool) ([]DNSRecord, error) {
	q := url.Values{
		"domain": {domain},
	}
	if zone != "" {
		q.Set("zone", zone)
	}
	if listZone {
		q.Set("listZone", "true")
	}

	_, body, err := c.request(ctx, "/api/zones/records/get", q)
	if err != nil {
		return nil, err
	}

	var resp getRecordsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decoding get-records response: %w", err)
	}
	return resp.Response.Records, nil
}

// currentAddresses returns the values of the recordType (A or AAAA) records
// that exist at exactly domain.
func (c *Client) currentAddresses(ctx context.Context, domain, zone, recordType string) ([]string, error) {
	records, err := c.GetRecords(ctx, domain, zone, false)
	if err != nil {
		return nil, fmt.Errorf("looking up current %s records for %s: %w", recordType, domain, err)
	}
	want := strings.TrimSuffix(domain, ".")
	var ips []string
	for _, rec := range records {
		if !strings.EqualFold(rec.Type, recordType) || !strings.EqualFold(strings.TrimSuffix(rec.Name, "."), want) {
			continue
		}
		if ip, ok := rec.RData["ipAddress"].(string); ok && ip != "" {
			ips = append(ips, ip)
		}
	}
	return ips, nil
}

// replaceRecord applies an update whose current value is ambiguous (zero or
// several records at the old name) by deleting whatever is there and adding
// the desired record.
func (c *Client) replaceRecord(ctx context.Context, r UpdateRecordRequest, current []string) error {
	ip := r.NewIPAddress
	if ip == "" {
		return fmt.Errorf("cannot replace %s record for %s: no current or new address", r.Type, r.Domain)
	}
	for _, old := range current {
		if err := c.DeleteRecord(ctx, DeleteRecordRequest{Domain: r.Domain, Zone: r.Zone, Type: r.Type, IPAddress: old}); err != nil {
			return err
		}
	}
	domain := r.NewDomain
	if domain == "" {
		domain = r.Domain
	}
	return c.AddRecord(ctx, AddRecordRequest{
		Domain:        domain,
		Zone:          r.Zone,
		Type:          r.Type,
		TTL:           r.NewTTL,
		IPAddress:     ip,
		Overwrite:     true,
		PTR:           r.PTR,
		CreatePTRZone: r.CreatePTRZone,
		Comments:      r.Comments,
	})
}

// SetPTR points the reverse record for ip at name (an FQDN), replacing
// whatever the reverse zone has there now. The reverse zone itself must
// already exist on the server.
func (c *Client) SetPTR(ctx context.Context, ip, name string, ttl int) error {
	reverse, err := ReverseName(ip)
	if err != nil {
		return err
	}
	return c.AddRecord(ctx, AddRecordRequest{
		Domain:    reverse,
		Type:      "PTR",
		TTL:       ttl,
		PTRName:   name,
		Overwrite: true,
	})
}

// ReverseName returns the in-addr.arpa (IPv4) or ip6.arpa (IPv6) name for
// ip, e.g. 10.200.55.0 -> 0.55.200.10.in-addr.arpa.
func ReverseName(ip string) (string, error) {
	name, err := dnsReverseAddr(ip)
	if err != nil {
		return "", fmt.Errorf("no reverse name for %q: %w", ip, err)
	}
	return strings.TrimSuffix(name, "."), nil
}

func isAddressType(recordType string) bool {
	return strings.EqualFold(recordType, "A") || strings.EqualFold(recordType, "AAAA")
}

// ListZones returns every zone hosted on the server.
// NOTE: response shape not fully confirmed against a live server during
// development (see the similar disclaimer on UpdateRecordRequest) --
// verify against your Technitium server's API docs.
func (c *Client) ListZones(ctx context.Context) ([]ZoneInfo, error) {
	_, body, err := c.request(ctx, "/api/zones/list", url.Values{})
	if err != nil {
		return nil, err
	}
	var resp listZonesResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decoding list-zones response: %w", err)
	}
	return resp.Response.Zones, nil
}

// CreateZone creates a new zone. zoneType is a Technitium zone type such as
// "Primary" -- see /api/zones/create in the Technitium API docs.
func (c *Client) CreateZone(ctx context.Context, zone, zoneType string) error {
	if zoneType == "" {
		zoneType = "Primary"
	}
	q := url.Values{
		"zone": {zone},
		"type": {zoneType},
	}
	_, _, err := c.request(ctx, "/api/zones/create", q)
	return err
}

func cloneValues(v url.Values) url.Values {
	out := make(url.Values, len(v))
	for k, vals := range v {
		out[k] = append([]string(nil), vals...)
	}
	return out
}
