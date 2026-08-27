package unifi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/Ohgwen/on-netreg/internal/macaddr"
)

// FetchClients returns the current set of known clients, merging the
// active-client list (/stat/sta) with the historical known-client list
// (/stat/alluser) so devices that are asleep/offline are still included.
// Active clients take precedence for IP/name freshness.
func (a *API) FetchClients(ctx context.Context) ([]NetworkClient, error) {
	active, err := a.fetch(ctx, "/stat/sta")
	if err != nil {
		return nil, fmt.Errorf("fetching active clients: %w", err)
	}
	all, err := a.fetch(ctx, "/stat/alluser")
	if err != nil {
		return nil, fmt.Errorf("fetching known clients: %w", err)
	}

	byMAC := make(map[string]NetworkClient, len(all))
	for _, c := range all {
		nc := toNetworkClient(c, false)
		byMAC[nc.MAC] = nc
	}
	for _, c := range active {
		nc := toNetworkClient(c, true)
		byMAC[nc.MAC] = nc
	}

	out := make([]NetworkClient, 0, len(byMAC))
	for _, nc := range byMAC {
		out = append(out, nc)
	}
	return out, nil
}

func (a *API) fetch(ctx context.Context, path string) ([]apiClient, error) {
	body, err := a.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	var parsed apiResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decoding response from %s: %w", path, err)
	}
	if parsed.Meta.RC != "" && parsed.Meta.RC != "ok" {
		return nil, fmt.Errorf("unifi API error from %s: %s", path, parsed.Meta.Msg)
	}
	return parsed.Data, nil
}

func toNetworkClient(c apiClient, online bool) NetworkClient {
	ip := c.IP
	if c.UseFixedIP && c.FixedIP != "" {
		ip = c.FixedIP
	}
	return NetworkClient{
		MAC:       macaddr.Normalize(c.MAC),
		Name:      c.Name,
		Hostname:  c.Hostname,
		IP:        ip,
		IsWired:   c.IsWired,
		IsGuest:   c.IsGuest,
		IsFixedIP: c.UseFixedIP && c.FixedIP != "",
		NetworkID: c.NetworkID,
		Network:   c.Network,
		VLAN:      c.VLAN,
		Online:    online,
	}
}
