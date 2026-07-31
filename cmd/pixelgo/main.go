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

	// Backfill catalog indexes for pixels created before the index schema
	// existed. Versioned — a single GET on every boot after the first.
	if err := rs.ReindexPixels(ctx); err != nil {
		log.Fatalf("pixel reindex: %v", err)
	}

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

	// Retention worker: permanently expunges pixels soft-deleted more than
	// 30 days ago (models.PixelDeleteRetention). Hourly cadence is plenty —
	// the deadline is measured in days — and each run is index-driven
	// (ZRANGEBYSCORE on the purge queue), so idle runs cost one Redis call.
	workerCtx, stopWorker := context.WithCancel(context.Background())
	defer stopWorker()
	go runExpungeWorker(workerCtx, rs)

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

// runExpungeWorker drains the soft-delete purge queue once at boot and then
// hourly until ctx is cancelled. Failures are logged and retried on the next
// tick — an expunge that runs late is harmless; the data is already
// invisible and uncounted from the moment it was soft-deleted.
func runExpungeWorker(ctx context.Context, pixels store.PixelStore) {
	run := func() {
		runCtx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		n, err := pixels.ExpungeDuePixels(runCtx, time.Now().UTC())
		if err != nil {
			log.Printf("expunge worker: %v", err)
			return
		}
		if n > 0 {
			log.Printf("expunge worker: permanently removed %d pixel(s)", n)
		}
	}

	run()
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
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
