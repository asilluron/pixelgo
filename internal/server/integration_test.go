//go:build integration

// Package server integration test. Exercises the full signup → org → invite →
// accept flow end-to-end against real Supabase (Auth + Postgres) and real
// Redis. Default `go test ./...` skips this file; run with:
//
//	go test -tags=integration ./internal/server/...
//
// Requires a populated .env with SUPABASE_* and PIXELGO_REDIS_URL set.
package server_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/asilluron/pixelgo/internal/config"
	"github.com/asilluron/pixelgo/internal/server"
	"github.com/asilluron/pixelgo/internal/store"
	"github.com/asilluron/pixelgo/internal/supaauth"
	"github.com/asilluron/pixelgo/web"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

// TestSignupInviteAcceptFlow drives the wizard end-to-end with two fresh
// users. It creates its own fixtures and tears them down in t.Cleanup so
// re-running the test is idempotent.
func TestSignupInviteAcceptFlow(t *testing.T) {
	// `go test` runs with CWD set to the package dir. .env lives at the repo
	// root; load it explicitly so SUPABASE_* + redis URL are visible to the
	// config loader that runs next.
	_ = godotenv.Load("../../.env")
	cfg, err := config.Load()
	if err != nil {
		t.Skipf("integration: config.Load: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	rs, err := store.NewRedis(ctx, cfg.RedisURL)
	if err != nil {
		t.Fatalf("redis: %v", err)
	}
	t.Cleanup(func() { _ = rs.Close() })

	ps, err := store.NewPostgres(ctx, cfg.SupabaseDBURL)
	if err != nil {
		t.Fatalf("postgres: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	// Second pool used only for fixture cleanup + fetching fixtures. Supabase's
	// transaction pooler hands out backend connections per-transaction, so two
	// separate pgx pools will collide on prepared-statement names. Disable the
	// prepared-statement cache for this read-only helper pool to avoid that.
	poolCfg, err := pgxpool.ParseConfig(cfg.SupabaseDBURL)
	if err != nil {
		t.Fatalf("pgxpool parse: %v", err)
	}
	poolCfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		t.Fatalf("pgxpool: %v", err)
	}
	t.Cleanup(pool.Close)

	supa := supaauth.New(cfg.SupabaseURL, cfg.SupabaseAnonKey, cfg.SupabaseServiceRoleKey)
	jwts, err := supaauth.NewVerifier(ctx, cfg.SupabaseURL)
	if err != nil {
		t.Fatalf("jwt verifier: %v", err)
	}

	cfg.RateLimitDisabled = true
	srv, err := server.New(cfg, rs, ps, supa, jwts, web.Templates())
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// Unique emails so parallel / re-runs don't collide. example.com is a
	// reserved TLD so these addresses can never deliver real mail.
	stamp := time.Now().UnixNano()
	ownerEmail := fmt.Sprintf("pixelgo-it-owner-%d@example.com", stamp)
	inviteeEmail := fmt.Sprintf("pixelgo-it-invitee-%d@example.com", stamp)
	ownerID := signup(t, ts, pool, ownerEmail, "pw-owner-12345", "")
	t.Cleanup(func() { _ = supa.AdminDeleteUser(context.Background(), ownerID) })

	// Step 2: create the org.
	client := mustClient(t, ts, ownerEmail, "pw-owner-12345", "")
	orgName := fmt.Sprintf("pixelgo-it-%d", stamp)
	postForm(t, client, ts.URL+"/signup/org", url.Values{"name": {orgName}}, "/admin")

	var orgID string
	if err := pool.QueryRow(ctx,
		`select id::text from public.orgs where name = $1`, orgName,
	).Scan(&orgID); err != nil {
		t.Fatalf("lookup org: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`delete from public.orgs where id = $1`, orgID)
	})

	// Dashboard should now render.
	res := mustGet(t, client, ts.URL+"/admin")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("owner /admin = %d, want 200", res.StatusCode)
	}

	// Owner mints an invite.
	postForm(t, client, ts.URL+"/admin/invites",
		url.Values{"email": {inviteeEmail}, "role": {"editor"}}, "/admin?invited=1")

	var inviteToken string
	if err := pool.QueryRow(ctx,
		`select token from public.invites where email = $1 and org_id = $2`,
		inviteeEmail, orgID,
	).Scan(&inviteToken); err != nil {
		t.Fatalf("lookup invite: %v", err)
	}

	// Invitee signs up via the invite link (skips step 2).
	inviteeID := signup(t, ts, pool, inviteeEmail, "pw-invitee-12345", inviteToken)
	t.Cleanup(func() { _ = supa.AdminDeleteUser(context.Background(), inviteeID) })

	// Verify membership row was written.
	var gotRole string
	if err := pool.QueryRow(ctx,
		`select role::text from public.org_members
		 where user_id = $1 and org_id = $2`, inviteeID, orgID,
	).Scan(&gotRole); err != nil {
		t.Fatalf("lookup membership: %v", err)
	}
	if gotRole != "editor" {
		t.Fatalf("invitee role = %q, want editor", gotRole)
	}

	// Verify the invite is now marked accepted.
	var acceptedAt *time.Time
	if err := pool.QueryRow(ctx,
		`select accepted_at from public.invites where token = $1`, inviteToken,
	).Scan(&acceptedAt); err != nil {
		t.Fatalf("lookup invite accepted: %v", err)
	}
	if acceptedAt == nil {
		t.Fatalf("invite not marked accepted")
	}
}
