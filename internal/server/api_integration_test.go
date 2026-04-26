//go:build integration

// End-to-end test of the customer-facing JSON API. Signs up a fresh owner,
// mints a personal API key via the admin HTML route, creates a pixel, and
// drives /api/v1/* with the bearer token. Requires a populated .env (same
// prerequisites as integration_test.go).
//
//	go test -tags=integration ./internal/server/...
package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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

// TestAPIKeyRoundtrip exercises mint → /api/v1/me → /pixels → stats → revoke.
func TestAPIKeyRoundtrip(t *testing.T) {
	_ = godotenv.Load("../../.env")
	cfg, err := config.Load()
	if err != nil {
		t.Skipf("integration: config.Load: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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

	stamp := time.Now().UnixNano()
	ownerEmail := fmt.Sprintf("pixelgo-api-%d@example.com", stamp)
	ownerID := signup(t, ts, pool, ownerEmail, "pw-owner-12345", "")
	t.Cleanup(func() { _ = supa.AdminDeleteUser(context.Background(), ownerID) })

	client := mustClient(t, ts, ownerEmail, "pw-owner-12345", "")
	orgName := fmt.Sprintf("pixelgo-api-%d", stamp)
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

	// Mint a personal key. The plaintext token is passed back on the redirect
	// query string — we intercept it there rather than parsing the dashboard.
	token := mintKey(t, client, ts.URL, "ci-key", "personal")
	if !strings.HasPrefix(token, "pxg_pk_") {
		t.Fatalf("unexpected token shape: %q", token)
	}

	// Create a pixel we can query.
	postForm(t, client, ts.URL+"/admin/pixels",
		url.Values{"name": {"ci-pixel"}}, "/admin")

	// /api/v1/me reflects the resolved org.
	var me struct {
		Data struct {
			KeyType string `json:"key_type"`
			OrgID   string `json:"org_id"`
		} `json:"data"`
	}
	apiGetJSON(t, ts.URL+"/api/v1/me", token, http.StatusOK, &me)
	if me.Data.KeyType != "personal" || me.Data.OrgID != orgID {
		t.Fatalf("me = %+v, want personal/%s", me.Data, orgID)
	}

	// /api/v1/pixels returns exactly the pixel we created.
	var list struct {
		Data []struct {
			ID    string `json:"id"`
			OrgID string `json:"org_id"`
			Name  string `json:"name"`
		} `json:"data"`
		Meta struct {
			Count int `json:"count"`
		} `json:"meta"`
	}
	apiGetJSON(t, ts.URL+"/api/v1/pixels", token, http.StatusOK, &list)
	if list.Meta.Count != 1 || list.Data[0].Name != "ci-pixel" || list.Data[0].OrgID != orgID {
		t.Fatalf("pixels list = %+v, want one ci-pixel in org %s", list, orgID)
	}
	pixelID := list.Data[0].ID

	// Stats endpoint returns zeros (no hits yet) and the pixel id echoed back.
	var stats struct {
		Data struct {
			PixelID string `json:"pixel_id"`
			Total   int64  `json:"total"`
		} `json:"data"`
	}
	apiGetJSON(t, ts.URL+"/api/v1/pixels/"+pixelID+"/stats", token, http.StatusOK, &stats)
	if stats.Data.PixelID != pixelID {
		t.Fatalf("stats pixel_id = %q, want %q", stats.Data.PixelID, pixelID)
	}

	// Revoke via the admin route. Look up the key id from the DB (we don't
	// get it back on the mint redirect).
	var keyID string
	if err := pool.QueryRow(ctx,
		`select id::text from public.api_keys where user_id = $1 and name = 'ci-key'`,
		ownerID,
	).Scan(&keyID); err != nil {
		t.Fatalf("lookup key id: %v", err)
	}
	postForm(t, client, ts.URL+"/admin/api-keys/"+keyID+"/revoke", url.Values{}, "/admin")

	// The same token should now 401 — the prefix-lookup filters on revoked_at.
	res := apiGet(t, ts.URL+"/api/v1/me", token)
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("after revoke, /me = %d, want 401", res.StatusCode)
	}
}

// mintKey POSTs /admin/api-keys and pulls the plaintext token out of the
// redirect's ?new_key=… parameter. The admin handler only surfaces the token
// this way (it's never stored in plaintext), so intercepting the 302 is the
// canonical read.
func mintKey(t *testing.T, client *http.Client, base, name, typ string) string {
	t.Helper()
	res, err := client.PostForm(base+"/admin/api-keys",
		url.Values{"name": {name}, "type": {typ}})
	if err != nil {
		t.Fatalf("POST /admin/api-keys: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusFound {
		t.Fatalf("mint status = %d, want 302", res.StatusCode)
	}
	loc, err := url.Parse(res.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse redirect: %v", err)
	}
	tok := loc.Query().Get("new_key")
	if tok == "" {
		t.Fatalf("no new_key in redirect: %s", res.Header.Get("Location"))
	}
	return tok
}

// apiGet issues an authenticated GET against the JSON API.
func apiGet(t *testing.T, u, token string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", u, err)
	}
	return res
}

// apiGetJSON is apiGet + status assertion + JSON decode.
func apiGetJSON(t *testing.T, u, token string, wantStatus int, into any) {
	t.Helper()
	res := apiGet(t, u, token)
	defer res.Body.Close()
	if res.StatusCode != wantStatus {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("GET %s = %d, want %d; body=%s", u, res.StatusCode, wantStatus, body)
	}
	if err := json.NewDecoder(res.Body).Decode(into); err != nil {
		t.Fatalf("decode %s: %v", u, err)
	}
}
