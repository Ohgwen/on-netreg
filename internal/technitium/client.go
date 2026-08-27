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
		"zone":   {r.Zone},
		"type":   {r.Type},
	}
	if r.TTL > 0 {
		q.Set("ttl", strconv.Itoa(r.TTL))
	}
	if r.IPAddress != "" {
		q.Set("ipAddress", r.IPAddress)
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

func (c *Client) UpdateRecord(ctx context.Context, r UpdateRecordRequest) error {
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
	if r.Comments != "" {
		q.Set("comments", r.Comments)
	}

	_, _, err := c.request(ctx, "/api/zones/records/update", q)
	return err
}

func (c *Client) DeleteRecord(ctx context.Context, r DeleteRecordRequest) error {
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

func cloneValues(v url.Values) url.Values {
	out := make(url.Values, len(v))
	for k, vals := range v {
		out[k] = append([]string(nil), vals...)
	}
	return out
}
