package auth

import "testing"

func TestExtractGroupsFromArray(t *testing.T) {
	claims := map[string]any{"groups": []any{"netreg-admins", "everyone"}}
	got := extractGroups(claims, "groups")
	if len(got) != 2 || got[0] != "netreg-admins" || got[1] != "everyone" {
		t.Errorf("got %v", got)
	}
}

func TestExtractGroupsFromSingleString(t *testing.T) {
	claims := map[string]any{"groups": "netreg-admins"}
	got := extractGroups(claims, "groups")
	if len(got) != 1 || got[0] != "netreg-admins" {
		t.Errorf("got %v", got)
	}
}

func TestExtractGroupsMissingClaim(t *testing.T) {
	claims := map[string]any{}
	if got := extractGroups(claims, "groups"); got != nil {
		t.Errorf("got %v, want nil", got)
	}
}

func TestExtractGroupsCustomClaimName(t *testing.T) {
	claims := map[string]any{"roles": []any{"admin"}}
	got := extractGroups(claims, "roles")
	if len(got) != 1 || got[0] != "admin" {
		t.Errorf("got %v", got)
	}
}

func TestIsAdminMember(t *testing.T) {
	groups := []string{"everyone", "netreg-admins"}
	if !isAdminMember(groups, "netreg-admins") {
		t.Errorf("expected netreg-admins to be a member")
	}
	if isAdminMember(groups, "other-group") {
		t.Errorf("expected other-group to not be a member")
	}
	if isAdminMember(nil, "netreg-admins") {
		t.Errorf("expected nil groups to never match")
	}
}
