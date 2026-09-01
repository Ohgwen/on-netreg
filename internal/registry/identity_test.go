package registry

import (
	"testing"
	"time"
)

func alwaysAlive(string) bool  { return true }
func neverAlive(string) bool   { return false }
func onlyAlive(ip string) func(string) bool {
	return func(candidate string) bool { return candidate == ip }
}

func TestSelectActivePicksHighestPriorityAliveMember(t *testing.T) {
	now := time.Now()
	members := []MemberState{
		{MAC: "aa:bb:cc:dd:ee:01", IPAddress: "192.168.1.10", LastSeen: now, Priority: 0},
		{MAC: "aa:bb:cc:dd:ee:02", IPAddress: "192.168.1.20", LastSeen: now, Priority: 1},
	}

	active, fellBack := SelectActive(now, members, time.Hour, alwaysAlive)

	if active == nil || active.MAC != "aa:bb:cc:dd:ee:01" {
		t.Fatalf("expected highest-priority member selected, got %+v", active)
	}
	if fellBack {
		t.Errorf("expected fellBack=false when a member passes liveness")
	}
}

func TestSelectActiveFallsThroughToNextPriorityWhenFirstIsDead(t *testing.T) {
	now := time.Now()
	members := []MemberState{
		{MAC: "aa:bb:cc:dd:ee:01", IPAddress: "192.168.1.10", LastSeen: now, Priority: 0},
		{MAC: "aa:bb:cc:dd:ee:02", IPAddress: "192.168.1.20", LastSeen: now, Priority: 1},
	}

	active, fellBack := SelectActive(now, members, time.Hour, onlyAlive("192.168.1.20"))

	if active == nil || active.MAC != "aa:bb:cc:dd:ee:02" {
		t.Fatalf("expected fallback to the alive lower-priority member, got %+v", active)
	}
	if fellBack {
		t.Errorf("expected fellBack=false when a member (even a lower-priority one) passes liveness")
	}
}

func TestSelectActiveFallsBackToHighestPriorityWhenNoneAlive(t *testing.T) {
	now := time.Now()
	members := []MemberState{
		{MAC: "aa:bb:cc:dd:ee:01", IPAddress: "192.168.1.10", LastSeen: now, Priority: 0},
		{MAC: "aa:bb:cc:dd:ee:02", IPAddress: "192.168.1.20", LastSeen: now, Priority: 1},
	}

	active, fellBack := SelectActive(now, members, time.Hour, neverAlive)

	if active == nil || active.MAC != "aa:bb:cc:dd:ee:01" {
		t.Fatalf("expected fallback to highest-priority member, got %+v", active)
	}
	if !fellBack {
		t.Errorf("expected fellBack=true when no member passes liveness")
	}
}

func TestSelectActiveSkipsStaleMembers(t *testing.T) {
	now := time.Now()
	members := []MemberState{
		{MAC: "aa:bb:cc:dd:ee:01", IPAddress: "192.168.1.10", LastSeen: now.Add(-time.Hour), Priority: 0},
		{MAC: "aa:bb:cc:dd:ee:02", IPAddress: "192.168.1.20", LastSeen: now, Priority: 1},
	}

	active, _ := SelectActive(now, members, time.Minute, alwaysAlive)

	if active == nil || active.MAC != "aa:bb:cc:dd:ee:02" {
		t.Fatalf("expected the stale member skipped in favor of the fresh one, got %+v", active)
	}
}

func TestSelectActiveSkipsInvalidIPs(t *testing.T) {
	now := time.Now()
	members := []MemberState{
		{MAC: "aa:bb:cc:dd:ee:01", IPAddress: "", LastSeen: now, Priority: 0},
		{MAC: "aa:bb:cc:dd:ee:02", IPAddress: "192.168.1.20", LastSeen: now, Priority: 1},
	}

	active, _ := SelectActive(now, members, time.Hour, alwaysAlive)

	if active == nil || active.MAC != "aa:bb:cc:dd:ee:02" {
		t.Fatalf("expected the member with no valid IP skipped, got %+v", active)
	}
}

func TestSelectActiveReturnsNilWhenNoCandidates(t *testing.T) {
	now := time.Now()
	members := []MemberState{
		{MAC: "aa:bb:cc:dd:ee:01", IPAddress: "", LastSeen: now, Priority: 0},
	}

	active, fellBack := SelectActive(now, members, time.Hour, alwaysAlive)

	if active != nil {
		t.Fatalf("expected nil active member, got %+v", active)
	}
	if fellBack {
		t.Errorf("expected fellBack=false when there is no candidate at all")
	}
}
