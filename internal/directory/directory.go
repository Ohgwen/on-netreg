// Package directory looks up users in an LDAP directory so devices can be
// assigned to them.
package directory

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"strings"

	"github.com/go-ldap/ldap/v3"

	"github.com/Ohgwen/on-netreg/internal/config"
)

// ErrNotFound is returned by Lookup when no user has the given username.
var ErrNotFound = errors.New("user not found in directory")

// searchLimit caps how many entries a Search returns; it exists for
// autocomplete, so a handful is plenty.
const searchLimit = 15

type User struct {
	Username string `json:"username"`
	Name     string `json:"name"`
	Email    string `json:"email"`
}

// Directory is what the web handlers depend on; tests supply a fake.
type Directory interface {
	Search(ctx context.Context, query string) ([]User, error)
	Lookup(ctx context.Context, username string) (User, error)
}

// LDAP is a Directory backed by a real LDAP server. It opens a fresh
// connection per call: lookups are rare, human-driven requests, so pooling
// isn't worth the staleness/reconnect handling.
type LDAP struct {
	cfg config.LDAPConfig
}

func New(cfg config.LDAPConfig) *LDAP { return &LDAP{cfg: cfg} }

// searchFilter matches users whose username, display name or mail contains
// query (case-insensitive per the server's matching rules).
func (l *LDAP) searchFilter(query string) string {
	q := ldap.EscapeFilter(query)
	return fmt.Sprintf("(&%s(|(%s=*%s*)(%s=*%s*)(%s=*%s*)))",
		l.cfg.UserFilter,
		l.cfg.UsernameAttr, q,
		l.cfg.DisplayNameAttr, q,
		l.cfg.MailAttr, q)
}

func (l *LDAP) lookupFilter(username string) string {
	return fmt.Sprintf("(&%s(%s=%s))", l.cfg.UserFilter, l.cfg.UsernameAttr, ldap.EscapeFilter(username))
}

func (l *LDAP) connect(ctx context.Context) (*ldap.Conn, error) {
	tlsCfg := &tls.Config{InsecureSkipVerify: l.cfg.InsecureSkipVerify}
	conn, err := ldap.DialURL(l.cfg.URL,
		ldap.DialWithTLSConfig(tlsCfg),
		ldap.DialWithDialer(dialerFor(ctx, l.cfg)))
	if err != nil {
		return nil, fmt.Errorf("connecting to ldap: %w", err)
	}
	conn.SetTimeout(l.cfg.Timeout)
	if l.cfg.StartTLS && strings.HasPrefix(l.cfg.URL, "ldap://") {
		if err := conn.StartTLS(tlsCfg); err != nil {
			conn.Close()
			return nil, fmt.Errorf("ldap starttls: %w", err)
		}
	}
	if l.cfg.BindDN != "" {
		if err := conn.Bind(l.cfg.BindDN, l.cfg.BindPass); err != nil {
			conn.Close()
			return nil, fmt.Errorf("ldap bind: %w", err)
		}
	}
	return conn, nil
}

func (l *LDAP) search(ctx context.Context, filter string, limit int) ([]User, error) {
	conn, err := l.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	res, err := conn.Search(ldap.NewSearchRequest(
		l.cfg.BaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases,
		limit, int(l.cfg.Timeout.Seconds()), false, filter,
		[]string{l.cfg.UsernameAttr, l.cfg.DisplayNameAttr, l.cfg.MailAttr}, nil))
	// A size-limit hit still returns the entries found so far, which is
	// exactly what autocomplete wants.
	if err != nil && !ldap.IsErrorWithCode(err, ldap.LDAPResultSizeLimitExceeded) {
		return nil, fmt.Errorf("ldap search: %w", err)
	}

	users := make([]User, 0, len(res.Entries))
	for _, e := range res.Entries {
		u := User{
			Username: e.GetAttributeValue(l.cfg.UsernameAttr),
			Name:     e.GetAttributeValue(l.cfg.DisplayNameAttr),
			Email:    e.GetAttributeValue(l.cfg.MailAttr),
		}
		if u.Username != "" {
			users = append(users, u)
		}
	}
	return users, nil
}

func (l *LDAP) Search(ctx context.Context, query string) ([]User, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	return l.search(ctx, l.searchFilter(query), searchLimit)
}

func (l *LDAP) Lookup(ctx context.Context, username string) (User, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return User{}, ErrNotFound
	}
	users, err := l.search(ctx, l.lookupFilter(username), 2)
	if err != nil {
		return User{}, err
	}
	// Attribute matching is case-insensitive, so prefer an exact-case hit
	// but accept a single case-insensitive one; several distinct matches
	// would be ambiguous.
	for _, u := range users {
		if u.Username == username {
			return u, nil
		}
	}
	if len(users) == 1 {
		return users[0], nil
	}
	return User{}, ErrNotFound
}
