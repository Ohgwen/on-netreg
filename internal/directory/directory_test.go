package directory

import (
	"testing"

	"github.com/Ohgwen/on-netreg/internal/config"
)

func testLDAP() *LDAP {
	return New(config.LDAPConfig{
		UserFilter:      "(objectClass=person)",
		UsernameAttr:    "uid",
		DisplayNameAttr: "cn",
		MailAttr:        "mail",
	})
}

func TestSearchFilter(t *testing.T) {
	got := testLDAP().searchFilter("ali")
	want := "(&(objectClass=person)(|(uid=*ali*)(cn=*ali*)(mail=*ali*)))"
	if got != want {
		t.Errorf("searchFilter = %q, want %q", got, want)
	}
}

func TestFiltersEscapeInput(t *testing.T) {
	l := testLDAP()
	if got := l.lookupFilter("a)(uid=*"); got != `(&(objectClass=person)(uid=a\29\28uid=\2a))` {
		t.Errorf("lookupFilter did not escape injection: %q", got)
	}
	if got := l.searchFilter("*)("); got != `(&(objectClass=person)(|(uid=*\2a\29\28*)(cn=*\2a\29\28*)(mail=*\2a\29\28*)))` {
		t.Errorf("searchFilter did not escape injection: %q", got)
	}
}
