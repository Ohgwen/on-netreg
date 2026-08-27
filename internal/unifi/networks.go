package unifi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// FetchNetworks returns the networks/VLANs configured on this controller's
// site, used to map clients to a DNS zone by which network they're on.
func (a *API) FetchNetworks(ctx context.Context) ([]Network, error) {
	body, err := a.do(ctx, http.MethodGet, "/rest/networkconf", nil)
	if err != nil {
		return nil, fmt.Errorf("fetching networks: %w", err)
	}

	var parsed networkConfResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decoding networks response: %w", err)
	}
	if parsed.Meta.RC != "" && parsed.Meta.RC != "ok" {
		return nil, fmt.Errorf("unifi API error from /rest/networkconf: %s", parsed.Meta.Msg)
	}

	out := make([]Network, 0, len(parsed.Data))
	for _, n := range parsed.Data {
		net := Network{
			ID:       n.ID,
			Name:     n.Name,
			VLAN:     n.VLAN,
			IPSubnet: n.IPSubnet,
		}
		if n.DHCPDEnabled {
			net.DHCPLeaseTimeSeconds = n.DHCPLeaseTime
		}
		out = append(out, net)
	}
	return out, nil
}
