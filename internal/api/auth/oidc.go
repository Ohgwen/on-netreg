// Package auth implements login via a generic OIDC provider (authorization
// code + PKCE). Any user who successfully authenticates gets full access
// to the dashboard -- there is no role/claim-based authorization.
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

	return &Authenticator{
		oauth2Config: oauth2Config,
		verifier:     provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		sessions:     store,
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

// CurrentUser returns the display name/email of the logged-in user, or ""
// if the request has no valid session.
func (a *Authenticator) CurrentUser(r *http.Request) string {
	sess, _ := a.sessions.Get(r, sessionName)
	user, _ := sess.Values["user"].(string)
	return user
}

func randString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
