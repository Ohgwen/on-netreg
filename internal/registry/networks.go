package registry

import (
	"net"

	"github.com/Ohgwen/on-netreg/internal/db"
)

// NetworkMatcher decides which network a device is on from its IP address
// alone: the known network (imported from UniFi or added by hand) whose
// subnet most specifically contains the address. UniFi's per-client
// network_id is deliberately not used -- it can be stale or wrong (e.g. a
// client moved VLANs but the controller still reports the old one), while
// the address a device actually holds can't lie about its subnet.
type NetworkMatcher struct {
	entries []networkEntry
}

type networkEntry struct {
	network db.UnifiNetwork
	subnet  *net.IPNet
	ones    int
}

// NewNetworkMatcher indexes networks by subnet. A subnet is CIDR notation,
// either as UniFi reports it (the gateway address plus prefix, e.g.
// "10.0.20.1/24") or as an admin enters it ("10.0.20.0/24"). Networks with
// no or an unparseable subnet can never match and are skipped.
func NewNetworkMatcher(networks []db.UnifiNetwork) *NetworkMatcher {
	m := &NetworkMatcher{}
	for _, n := range networks {
		_, subnet, err := net.ParseCIDR(n.IPSubnet)
		if err != nil {
			continue
		}
		ones, _ := subnet.Mask.Size()
		m.entries = append(m.entries, networkEntry{network: n, subnet: subnet, ones: ones})
	}
	return m
}

// Match returns the network ip belongs to. The longest matching prefix wins;
// among equally specific subnets (the same range defined twice), a Manual
// network beats an imported one, then a network imported from controllerID
// (the console the device was seen on) beats one from another console, then
// the lowest ID wins so the answer is stable.
func (m *NetworkMatcher) Match(ip string, controllerID uint) (db.UnifiNetwork, bool) {
	parsed := net.ParseIP(ip)
	if parsed == nil || m == nil {
		return db.UnifiNetwork{}, false
	}
	var best *networkEntry
	for i := range m.entries {
		e := &m.entries[i]
		if !e.subnet.Contains(parsed) {
			continue
		}
		if best == nil || betterMatch(*e, *best, controllerID) {
			best = e
		}
	}
	if best == nil {
		return db.UnifiNetwork{}, false
	}
	return best.network, true
}

func betterMatch(a, b networkEntry, controllerID uint) bool {
	if a.ones != b.ones {
		return a.ones > b.ones
	}
	if a.network.Manual != b.network.Manual {
		return a.network.Manual
	}
	aLocal, bLocal := a.network.ControllerID == controllerID, b.network.ControllerID == controllerID
	if aLocal != bLocal {
		return aLocal
	}
	return a.network.ID < b.network.ID
}
