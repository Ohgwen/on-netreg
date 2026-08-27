// Package unifi is a client for the UniFi OS local Network API (UDM,
// UDM-Pro, Cloud Gateway) used to fetch the current client list.
package unifi

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync"

	"github.com/Ohgwen/on-netreg/internal/config"
)

// API is a session-authenticated client for one UniFi OS console.
type API struct {
	baseURL  string
	site     string
	username string
	password string

	httpClient *http.Client

	mu        sync.Mutex
	csrfToken string
	loggedIn  bool
}

func New(cfg config.UnifiConfig) (*API, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("creating cookie jar: %w", err)
	}

	transport := &http.Transport{}
	if cfg.InsecureSkipVerify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // opt-in for self-signed UDM certs
	}

	return &API{
		baseURL:  strings.TrimRight(cfg.BaseURL, "/"),
		site:     cfg.Site,
		username: cfg.Username,
		password: cfg.Password,
		httpClient: &http.Client{
			Jar:       jar,
			Transport: transport,
		},
	}, nil
}

// login performs the UniFi OS auth flow and stores the CSRF token for
// subsequent requests. It does not support 2FA-enabled accounts; use a
// dedicated local admin account without 2FA for this integration.
func (a *API) login(ctx context.Context) error {
	body, err := json.Marshal(loginRequest{
		Username: a.username,
		Password: a.password,
		Remember: true,
	})
	if err != nil {
		return fmt.Errorf("encoding login request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/api/auth/login", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("login request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 499 {
		return fmt.Errorf("unifi login requires 2FA, which this client does not support; use an account without 2FA")
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unifi login failed: %s: %s", resp.Status, string(respBody))
	}

	a.mu.Lock()
	if token := resp.Header.Get("X-CSRF-Token"); token != "" {
		a.csrfToken = token
	} else if token := resp.Header.Get("X-Updated-CSRF-Token"); token != "" {
		a.csrfToken = token
	}
	a.loggedIn = true
	a.mu.Unlock()

	return nil
}

// do executes an authenticated request against the UniFi Network API,
// logging in first if necessary and retrying once after a fresh login on a
// 401 (expired session).
func (a *API) do(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	a.mu.Lock()
	needsLogin := !a.loggedIn
	a.mu.Unlock()
	if needsLogin {
		if err := a.login(ctx); err != nil {
			return nil, err
		}
	}

	respBody, status, err := a.doOnce(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	if status == http.StatusUnauthorized {
		if err := a.login(ctx); err != nil {
			return nil, err
		}
		respBody, status, err = a.doOnce(ctx, method, path, body)
		if err != nil {
			return nil, err
		}
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("unifi API request to %s failed: status %d: %s", path, status, string(respBody))
	}
	return respBody, nil
}

func (a *API) doOnce(ctx context.Context, method, path string, body []byte) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, a.baseURL+"/proxy/network/api/s/"+a.site+path, reader)
	if err != nil {
		return nil, 0, fmt.Errorf("building request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	a.mu.Lock()
	if a.csrfToken != "" {
		req.Header.Set("X-CSRF-Token", a.csrfToken)
	}
	a.mu.Unlock()

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	if token := resp.Header.Get("X-Updated-CSRF-Token"); token != "" {
		a.mu.Lock()
		a.csrfToken = token
		a.mu.Unlock()
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("reading response body: %w", err)
	}
	return respBody, resp.StatusCode, nil
}
