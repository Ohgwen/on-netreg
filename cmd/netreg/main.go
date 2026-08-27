// Command netreg runs the ON-Netreg server: the background UniFi->Technitium
// sync engine and the web dashboard (OIDC-authenticated by default; see
// server.auth_enabled / -disable-auth).
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
		// requirement for oidc.issuer_url/client_id/session_secret.
		os.Setenv("NETREG_SERVER_AUTH_ENABLED", "false")
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	if !cfg.Server.AuthEnabled {
		logger.Warn("authentication is DISABLED (server.auth_enabled=false) -- the dashboard is unauthenticated; do not expose this instance to an untrusted network")
	}

	gdb, err := db.Open(cfg.Database)
	if err != nil {
		return err
	}

	unifiClient, err := unifi.New(cfg.Unifi)
	if err != nil {
		return err
	}
	dnsClient := technitium.New(cfg.Technitium)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var authenticator *auth.Authenticator
	currentUser := func(*http.Request) string { return "" }
	authMiddleware := func(next http.Handler) http.Handler { return next }

	if cfg.Server.AuthEnabled {
		authenticator, err = auth.New(ctx, cfg.OIDC, cfg.Server.SessionSecret)
		if err != nil {
			return err
		}
		currentUser = authenticator.CurrentUser
		authMiddleware = authenticator.Middleware
	}

	engine := &sync.Engine{
		DB:     gdb,
		Unifi:  unifiClient,
		DNS:    dnsClient,
		Config: cfg,
		Logger: logger,
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

	h := &handlers.Handlers{
		DB:          gdb,
		Engine:      engine,
		DNS:         dnsClient,
		Zone:        cfg.Technitium.Zone,
		Pages:       pages,
		Logger:      logger,
		CurrentUser: currentUser,
	}

	mux := http.NewServeMux()
	if authenticator != nil {
		mux.HandleFunc("GET /login", authenticator.LoginHandler)
		mux.HandleFunc("GET /auth/callback", authenticator.CallbackHandler)
		mux.HandleFunc("GET /logout", authenticator.LogoutHandler)
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticFS)))
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
