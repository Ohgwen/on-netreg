package registry

import "time"

// MemberState is what SelectActive needs to know about one Identity member
// to decide whether it's a viable candidate to back the shared DNS record.
type MemberState struct {
	MAC       string
	IPAddress string
	LastSeen  time.Time
	// Priority is the member's configured priority (lower = tried first).
	// Members must already be sorted by Priority before being passed in.
	Priority int
}

// SelectActive picks which Identity member should currently back the
// shared DNS record. It walks members in priority order (the order they're
// passed in -- callers sort by Priority beforehand) and returns the first
// one that has a valid IP, was seen within freshWindow of now, and passes
// isAlive. If no member passes isAlive, it falls back to the
// highest-priority fresh candidate anyway (so the record doesn't go empty
// just because a liveness probe failed) via the ok=false-equivalent
// fellBack return. If no member has a valid, fresh IP at all, it returns
// nil.
//
// isAlive is injected so tests don't need a real network/ping; it's only
// consulted for members that are otherwise valid candidates.
func SelectActive(now time.Time, members []MemberState, freshWindow time.Duration, isAlive func(ip string) bool) (active *MemberState, fellBackToUnverified bool) {
	var fallback *MemberState

	for i := range members {
		m := &members[i]
		if !HasValidIP(m.IPAddress) {
			continue
		}
		if freshWindow > 0 && now.Sub(m.LastSeen) > freshWindow {
			continue
		}
		if fallback == nil {
			fallback = m
		}
		if isAlive(m.IPAddress) {
			return m, false
		}
	}

	if fallback != nil {
		return fallback, true
	}
	return nil, false
}
