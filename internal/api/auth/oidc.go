// Package auth implements login via a generic OIDC provider (authorization
// code + PKCE). Any user who successfully authenticates gets access to the
// dashboard; access to the admin-only Settings area additionally requires
// membership in a configured group claim (see OIDCConfig.AdminGroup).
package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/gorilla/sessions"
	"golang.org/x/oauth2"

	"github.com/Ohgwen/on-netreg/internal/config"
)

const sessionName = "netreg_session"

type Authenticator struct {
	oauth2Config oauth2.Config
	verifier     *oidc.IDTokenVerifier
	sessions     *sessions.CookieStore

	groupsClaim string
	adminGroup  string
}

func New(ctx context.Context, cfg config.OIDCConfig, sessionSecret string) (*Authenticator, error) {
	provider, err := oidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("discovering OIDC provider: %w", err)
	}

	oauth2Config := oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectURL:  cfg.RedirectURL,
		Endpoint:     provider.Endpoint(),
		Scopes:       cfg.Scopes,
	}

	store := sessions.NewCookieStore([]byte(sessionSecret))
	store.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   7 * 24 * 60 * 60,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}

	groupsClaim := cfg.GroupsClaim
	if groupsClaim == "" {
		groupsClaim = "groups"
	}

	return &Authenticator{
		oauth2Config: oauth2Config,
		verifier:     provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		sessions:     store,
		groupsClaim:  groupsClaim,
		adminGroup:   cfg.AdminGroup,
	}, nil
}

func (a *Authenticator) LoginHandler(w http.ResponseWriter, r *http.Request) {
	state, err := randString(32)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	verifier := oauth2.GenerateVerifier()

	sess, _ := a.sessions.Get(r, sessionName)
	sess.Values["oauth_state"] = state
	sess.Values["pkce_verifier"] = verifier
	if err := sess.Save(r, w); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	authURL := a.oauth2Config.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier))
	http.Redirect(w, r, authURL, http.StatusFound)
}

func (a *Authenticator) CallbackHandler(w http.ResponseWriter, r *http.Request) {
	sess, _ := a.sessions.Get(r, sessionName)

	state, _ := sess.Values["oauth_state"].(string)
	verifier, _ := sess.Values["pkce_verifier"].(string)
	if state == "" || r.URL.Query().Get("state") != state {
		http.Error(w, "invalid oauth state", http.StatusBadRequest)
		return
	}

	token, err := a.oauth2Config.Exchange(r.Context(), r.URL.Query().Get("code"), oauth2.VerifierOption(verifier))
	if err != nil {
		http.Error(w, "token exchange failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		http.Error(w, "no id_token in token response", http.StatusInternalServerError)
		return
	}
	idToken, err := a.verifier.Verify(r.Context(), rawIDToken)
	if err != nil {
		http.Error(w, "id token verification failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var claims struct {
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	_ = idToken.Claims(&claims)

	var rawClaims map[string]any
	_ = idToken.Claims(&rawClaims)
	groups := extractGroups(rawClaims, a.groupsClaim)
	isAdmin := a.adminGroup == "" || isAdminMember(groups, a.adminGroup)

	user := claims.Email
	if user == "" {
		user = claims.Name
	}
	if user == "" {
		user = idToken.Subject
	}

	delete(sess.Values, "oauth_state")
	delete(sess.Values, "pkce_verifier")
	sess.Values["authenticated"] = true
	sess.Values["user"] = user
	sess.Values["is_admin"] = isAdmin
	if err := sess.Save(r, w); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/", http.StatusFound)
}

func (a *Authenticator) LogoutHandler(w http.ResponseWriter, r *http.Request) {
	sess, _ := a.sessions.Get(r, sessionName)
	sess.Options.MaxAge = -1
	_ = sess.Save(r, w)
	http.Redirect(w, r, "/", http.StatusFound)
}

// Middleware redirects unauthenticated requests to /login.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, _ := a.sessions.Get(r, sessionName)
		if authed, _ := sess.Values["authenticated"].(bool); !authed {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireAdmin 403s any request from a user who isn't a member of the
// configured admin group. Meant to wrap only the Settings routes, inside
// Middleware.
func (a *Authenticator) RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.CurrentUserIsAdmin(r) {
			http.Error(w, "forbidden: Settings requires membership in the configured admin group", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// CurrentUser returns the display name/email of the logged-in user, or ""
// if the request has no valid session.
func (a *Authenticator) CurrentUser(r *http.Request) string {
	sess, _ := a.sessions.Get(r, sessionName)
	user, _ := sess.Values["user"].(string)
	return user
}

// CurrentUserIsAdmin reports whether the logged-in user is a member of the
// configured admin group (or whether no admin group is configured, in
// which case every authenticated user counts as admin).
func (a *Authenticator) CurrentUserIsAdmin(r *http.Request) bool {
	sess, _ := a.sessions.Get(r, sessionName)
	isAdmin, _ := sess.Values["is_admin"].(bool)
	return isAdmin
}

// extractGroups looks up claimName in rawClaims and normalizes it to a
// string slice: OIDC providers variously report group membership as a JSON
// array of strings or, for a single group, a bare string.
func extractGroups(rawClaims map[string]any, claimName string) []string {
	v, ok := rawClaims[claimName]
	if !ok {
		return nil
	}
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		return []string{t}
	default:
		return nil
	}
}

// isAdminMember reports whether target appears (exact match) in groups.
func isAdminMember(groups []string, target string) bool {
	for _, g := range groups {
		if g == target {
			return true
		}
	}
	return false
}

func randString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
