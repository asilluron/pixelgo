// Command pixelgo is a high-throughput tracking-pixel server with an admin UI.
package main

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/asilluron/pixelgo/internal/config"
	"github.com/asilluron/pixelgo/internal/server"
	"github.com/asilluron/pixelgo/internal/store"
	"github.com/asilluron/pixelgo/internal/supaauth"
	"github.com/asilluron/pixelgo/web"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rs, err := store.NewRedis(ctx, cfg.RedisURL)
	if err != nil {
		log.Fatalf("redis: %v", err)
	}
	defer func() { _ = rs.Close() }()

	ps, err := store.NewPostgres(ctx, cfg.SupabaseDBURL)
	if err != nil {
		log.Fatalf("postgres: %v", err)
	}
	defer func() { _ = ps.Close() }()

	supa := supaauth.New(cfg.SupabaseURL, cfg.SupabaseAnonKey, cfg.SupabaseServiceRoleKey)
	jwts, err := supaauth.NewVerifier(ctx, cfg.SupabaseURL)
	if err != nil {
		log.Fatalf("jwt verifier: %v", err)
	}

	if err := bootstrap(context.Background(), ps, supa, cfg); err != nil {
		log.Fatalf("bootstrap: %v", err)
	}

	// Dev opt-in: load templates from disk so edits hot-reload without a
	// rebuild. The dir is expected to contain the *.html files directly.
	var tmplFS fs.FS = web.Templates()
	if cfg.TemplatesDir != "" {
		tmplFS = os.DirFS(cfg.TemplatesDir)
	}

	srv, err := server.New(cfg, rs, ps, supa, jwts, tmplFS)
	if err != nil {
		log.Fatalf("server: %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("pixelgo listening on %s (env=%s)", cfg.Addr, cfg.Env)
		if err := srv.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	select {
	case err := <-errCh:
		log.Fatalf("server error: %v", err)
	case sig := <-sigCh:
		log.Printf("received %s, shutting down", sig)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("shutdown: %v", err)
	}
}

// bootstrap provisions the super-admin on first boot if no profile with
// is_super_admin=true exists. The user is created in Supabase Auth via the
// Admin API (so they're email-confirmed immediately) and flagged in our
// own profiles table. It's a no-op on subsequent boots.
func bootstrap(ctx context.Context, auth store.AuthStore, supa *supaauth.Client, cfg *config.Config) error {
	n, err := auth.CountSuperAdmins(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	if cfg.BootstrapPassword == "" || cfg.BootstrapEmail == "" {
		log.Printf("skipping bootstrap: PIXELGO_BOOTSTRAP_EMAIL/PASSWORD not set")
		return nil
	}
	if cfg.SupabaseServiceRoleKey == "" {
		log.Printf("skipping bootstrap: SUPABASE_SERVICE_ROLE_KEY not set")
		return nil
	}

	u, err := supa.AdminCreateUser(ctx, cfg.BootstrapEmail, cfg.BootstrapPassword)
	if err != nil {
		return err
	}
	if err := auth.UpsertProfile(ctx, u.ID, true); err != nil {
		return err
	}
	log.Printf("bootstrap: created super-admin %s", u.Email)
	return nil
}
