package unifi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/Ohgwen/on-netreg/internal/macaddr"
)

type userResponse struct {
	Meta struct {
		RC  string `json:"rc"`
		Msg string `json:"msg"`
	} `json:"meta"`
	Data []struct {
		ID string `json:"_id"`
	} `json:"data"`
}

// SetFixedIP reserves ip for the client with the given MAC on the UniFi
// network networkID (the controller's network _id) -- the same fields the
// UniFi UI's "Fixed IP Address" toggle writes. The client record is created
// if the controller doesn't know the MAC yet, so a device can be reserved
// before it first connects.
func (a *API) SetFixedIP(ctx context.Context, mac, ip, networkID string) error {
	return a.writeUser(ctx, mac, map[string]any{"use_fixedip": true, "fixed_ip": ip, "network_id": networkID})
}

// ClearFixedIP removes any DHCP reservation for the client with the given
// MAC, returning it to a dynamic lease. A MAC the controller doesn't know
// has nothing to clear.
func (a *API) ClearFixedIP(ctx context.Context, mac string) error {
	id, err := a.userID(ctx, mac)
	if err != nil || id == "" {
		return err
	}
	return a.putUser(ctx, id, map[string]any{"use_fixedip": false})
}

// SetName sets the client's alias (the name shown in the UniFi UI) for the
// client with the given MAC, creating the client record if needed.
func (a *API) SetName(ctx context.Context, mac, name string) error {
	return a.writeUser(ctx, mac, map[string]any{"name": name})
}

// writeUser applies fields to the known client with the given MAC, or
// creates a client record for it if the controller has none.
func (a *API) writeUser(ctx context.Context, mac string, fields map[string]any) error {
	id, err := a.userID(ctx, mac)
	if err != nil {
		return err
	}
	if id != "" {
		return a.putUser(ctx, id, fields)
	}

	fields["mac"] = macaddr.Normalize(mac)
	body, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("encoding client update: %w", err)
	}
	resp, err := a.do(ctx, http.MethodPost, "/rest/user", body)
	if err != nil {
		return fmt.Errorf("creating client %s: %w", mac, err)
	}
	return checkMeta(resp, "/rest/user")
}

func (a *API) putUser(ctx context.Context, id string, fields map[string]any) error {
	body, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("encoding client update: %w", err)
	}
	path := "/rest/user/" + id
	resp, err := a.do(ctx, http.MethodPut, path, body)
	if err != nil {
		return fmt.Errorf("updating client %s: %w", id, err)
	}
	return checkMeta(resp, path)
}

// userID returns the controller's _id for the known client with the given
// MAC, or "" if the controller has no record of it.
// NOTE: controllers have been seen to answer an unknown MAC with either an
// empty data list or an api.err.UnknownUser error; both are treated as "not
// found". Not confirmed across firmware versions.
func (a *API) userID(ctx context.Context, mac string) (string, error) {
	path := "/stat/user/" + macaddr.Normalize(mac)
	body, err := a.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		if strings.Contains(err.Error(), "UnknownUser") {
			return "", nil
		}
		return "", fmt.Errorf("looking up client %s: %w", mac, err)
	}
	var parsed userResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("decoding response from %s: %w", path, err)
	}
	if len(parsed.Data) == 0 {
		return "", nil
	}
	return parsed.Data[0].ID, nil
}

func checkMeta(body []byte, path string) error {
	var parsed userResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return fmt.Errorf("decoding response from %s: %w", path, err)
	}
	if parsed.Meta.RC != "" && parsed.Meta.RC != "ok" {
		return fmt.Errorf("unifi API error from %s: %s", path, parsed.Meta.Msg)
	}
	return nil
}
