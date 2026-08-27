// Command netreg runs the ON-Netreg server: the background multi-controller
// UniFi->Technitium sync engine and the web dashboard (OIDC-authenticated by
// default; see server.auth_enabled / -disable-auth).
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Ohgwen/on-netreg/internal/api/auth"
	"github.com/Ohgwen/on-netreg/internal/api/handlers"
	"github.com/Ohgwen/on-netreg/internal/api/web"
	"github.com/Ohgwen/on-netreg/internal/config"
	"github.com/Ohgwen/on-netreg/internal/db"
	"github.com/Ohgwen/on-netreg/internal/logging"
	"github.com/Ohgwen/on-netreg/internal/settings"
	"github.com/Ohgwen/on-netreg/internal/sync"
	"github.com/Ohgwen/on-netreg/internal/technitium"
	"github.com/Ohgwen/on-netreg/internal/unifi"
)

func main() {
	configPath := flag.String("config", os.Getenv("NETREG_CONFIG"), "path to config YAML file")
	disableAuth := flag.Bool("disable-auth", false, "disable OIDC login entirely, serving the dashboard with no authentication (INSECURE -- local/dev use on a trusted network only). Overrides server.auth_enabled.")
	flag.Parse()

	logger := logging.New()

	if err := run(*configPath, *disableAuth, logger); err != nil {
		logger.Error("fatal error", "error", err)
		os.Exit(1)
	}
}

func run(configPath string, disableAuth bool, logger *slog.Logger) error {
	if disableAuth {
		// Applied before Load so it also short-circuits Validate()'s
		// requirement for oidc.issuer_url/client_id.
		os.Setenv("NETREG_SERVER_AUTH_ENABLED", "false")
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	if !cfg.Server.AuthEnabled {
		logger.Warn("authentication is DISABLED (server.auth_enabled=false) -- the dashboard is unauthenticated and every visitor has Settings access; do not expose this instance to an untrusted network")
	}

	gdb, err := db.Open(cfg.Database)
	if err != nil {
		return err
	}

	secretKey := settings.Key(cfg.Server.SessionSecret)
	if err := settings.SeedFromConfig(gdb, secretKey, cfg); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var authenticator *auth.Authenticator
	currentUser := func(*http.Request) string { return "" }
	// With auth disabled there's no login/no role system at all, so every
	// visitor is treated as admin -- matches this app's existing "any
	// logged-in user gets full access" behavior for the local-dev case.
	currentUserIsAdmin := func(*http.Request) bool { return true }
	authMiddleware := func(next http.Handler) http.Handler { return next }
	adminMiddleware := func(next http.Handler) http.Handler { return next }

	if cfg.Server.AuthEnabled {
		authenticator, err = auth.New(ctx, cfg.OIDC, cfg.Server.SessionSecret)
		if err != nil {
			return err
		}
		currentUser = authenticator.CurrentUser
		currentUserIsAdmin = authenticator.CurrentUserIsAdmin
		authMiddleware = authenticator.Middleware
		adminMiddleware = authenticator.RequireAdmin
	}

	engine := &sync.Engine{
		DB:        gdb,
		Logger:    logger,
		SecretKey: secretKey,
		NewUnifiClient: func(c config.UnifiConfig) (sync.UnifiClient, error) {
			return unifi.New(c)
		},
		NewDNSClient: func(c config.TechnitiumConfig) sync.DNSClient {
			return technitium.New(c)
		},
	}
	go engine.Run(ctx)

	pages, err := web.Templates()
	if err != nil {
		return err
	}
	staticFS, err := web.Static()
	if err != nil {
		return err
	}

	dnsClientFactory := func(ctx context.Context) (handlers.DNSClient, error) {
		technitiumCfg, err := settings.LoadTechnitium(gdb, secretKey)
		if err != nil {
			return nil, err
		}
		return technitium.New(technitiumCfg), nil
	}

	h := &handlers.Handlers{
		DB:          gdb,
		Engine:      engine,
		DNS:         dnsClientFactory,
		Pages:       pages,
		Logger:      logger,
		CurrentUser: currentUser,
		IsAdmin:     currentUserIsAdmin,
	}

	settingsHandlers := &handlers.SettingsHandlers{
		DB:          gdb,
		SecretKey:   secretKey,
		Pages:       pages,
		Logger:      logger,
		CurrentUser: currentUser,
		IsAdmin:     currentUserIsAdmin,
	}

	mux := http.NewServeMux()
	if authenticator != nil {
		mux.HandleFunc("GET /login", authenticator.LoginHandler)
		mux.HandleFunc("GET /auth/callback", authenticator.CallbackHandler)
		mux.HandleFunc("GET /logout", authenticator.LogoutHandler)
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticFS)))
	mux.Handle("/settings/", authMiddleware(adminMiddleware(settingsHandlers.Routes())))
	mux.Handle("/", authMiddleware(h.Routes()))

	srv := &http.Server{
		Addr:    cfg.Server.ListenAddr,
		Handler: mux,
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("starting server", "addr", cfg.Server.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return <-serveErr
	case err := <-serveErr:
		return err
	}
}
