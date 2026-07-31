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

	// --- Dynamic creation via the API (quick pixel for a product page) ---
	var created struct {
		Data struct {
			ID    string   `json:"id"`
			OrgID string   `json:"org_id"`
			Name  string   `json:"name"`
			URL   string   `json:"url"`
			Tags  []string `json:"tags"`
		} `json:"data"`
	}
	apiDoJSON(t, http.MethodPost, ts.URL+"/api/v1/pixels", token,
		`{"name":"ci-product-page","url":"https://shop.example.com/products/1","tags":["Products","launch"]}`,
		http.StatusCreated, &created)
	if created.Data.ID == "" || created.Data.OrgID != orgID {
		t.Fatalf("create = %+v, want id set + org %s", created.Data, orgID)
	}
	if created.Data.URL != "https://shop.example.com/products/1" {
		t.Fatalf("create url = %q", created.Data.URL)
	}
	// Tags are normalized to lowercase.
	if len(created.Data.Tags) != 2 || created.Data.Tags[0] != "products" || created.Data.Tags[1] != "launch" {
		t.Fatalf("create tags = %v, want [products launch]", created.Data.Tags)
	}
	createdID := created.Data.ID

	// Validation: bad URL rejected.
	res := apiDo(t, http.MethodPost, ts.URL+"/api/v1/pixels", token, `{"name":"bad","url":"ftp://nope"}`)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad url create = %d, want 400", res.StatusCode)
	}

	// --- Catalog: filter + sort + paginate ---
	type pageResp struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
		Meta struct {
			Count int   `json:"count"`
			Total int64 `json:"total"`
		} `json:"meta"`
	}
	var page pageResp
	apiGetJSON(t, ts.URL+"/api/v1/pixels?tag=products", token, http.StatusOK, &page)
	if page.Meta.Total != 1 || page.Data[0].ID != createdID {
		t.Fatalf("tag filter = %+v, want just %s", page, createdID)
	}
	apiGetJSON(t, ts.URL+"/api/v1/pixels?q=ci-product", token, http.StatusOK, &page)
	if page.Meta.Total != 1 || page.Data[0].ID != createdID {
		t.Fatalf("prefix filter = %+v, want just %s", page, createdID)
	}
	apiGetJSON(t, ts.URL+"/api/v1/pixels?sort=name_asc", token, http.StatusOK, &page)
	if page.Meta.Total != 2 || page.Data[0].Name != "ci-pixel" || page.Data[1].Name != "ci-product-page" {
		t.Fatalf("name_asc = %+v, want [ci-pixel ci-product-page]", page)
	}
	apiGetJSON(t, ts.URL+"/api/v1/pixels?sort=name_asc&limit=1&offset=1", token, http.StatusOK, &page)
	if page.Meta.Count != 1 || page.Meta.Total != 2 || page.Data[0].ID != createdID {
		t.Fatalf("paginated page 2 = %+v, want just %s", page, createdID)
	}
	res = apiGet(t, ts.URL+"/api/v1/pixels?q=x&tag=y", token)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("q+tag combo = %d, want 400", res.StatusCode)
	}

	// --- Soft delete: 30-day retention window ---
	var del struct {
		Data struct {
			ID        string     `json:"id"`
			DeletedAt *time.Time `json:"deleted_at"`
			PurgeAt   *time.Time `json:"purge_at"`
		} `json:"data"`
	}
	apiDoJSON(t, http.MethodDelete, ts.URL+"/api/v1/pixels/"+createdID, token, "", http.StatusOK, &del)
	if del.Data.DeletedAt == nil || del.Data.PurgeAt == nil {
		t.Fatalf("delete = %+v, want deleted_at and purge_at set", del.Data)
	}
	if got := del.Data.PurgeAt.Sub(*del.Data.DeletedAt); got != 30*24*time.Hour {
		t.Fatalf("purge window = %v, want 720h", got)
	}
	// Idempotent: second delete returns the same deleted_at.
	var again struct {
		Data struct {
			DeletedAt *time.Time `json:"deleted_at"`
		} `json:"data"`
	}
	apiDoJSON(t, http.MethodDelete, ts.URL+"/api/v1/pixels/"+createdID, token, "", http.StatusOK, &again)
	if !again.Data.DeletedAt.Equal(*del.Data.DeletedAt) {
		t.Fatalf("second delete moved deleted_at: %v vs %v", again.Data.DeletedAt, del.Data.DeletedAt)
	}
	// Gone from live listings…
	apiGetJSON(t, ts.URL+"/api/v1/pixels", token, http.StatusOK, &page)
	if page.Meta.Total != 1 || page.Data[0].Name != "ci-pixel" {
		t.Fatalf("live list after delete = %+v, want just ci-pixel", page)
	}
	// …but visible in the deleted view until expunge.
	apiGetJSON(t, ts.URL+"/api/v1/pixels?status=deleted", token, http.StatusOK, &page)
	if page.Meta.Total != 1 || page.Data[0].ID != createdID {
		t.Fatalf("deleted list = %+v, want just %s", page, createdID)
	}
	// Hot path stops counting a deleted pixel.
	if _, err := http.Get(ts.URL + "/p/" + createdID); err != nil {
		t.Fatalf("GET /p/ deleted: %v", err)
	}
	time.Sleep(500 * time.Millisecond) // increment is async; give it time to (not) land
	if n, err := rs.GetPixelCount(ctx, createdID); err != nil || n != 0 {
		t.Fatalf("deleted pixel count = (%d, %v), want (0, nil)", n, err)
	}

	// --- Docs stay reachable from the logged-in dashboard menu ---
	dash := mustGet(t, client, ts.URL+"/admin")
	body, _ := io.ReadAll(dash.Body)
	if !strings.Contains(string(body), `href="/docs"`) {
		t.Fatalf("dashboard missing API docs link")
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
	res = apiGet(t, ts.URL+"/api/v1/me", token)
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

// apiDo issues an authenticated request with an optional JSON body.
func apiDo(t *testing.T, method, u, token, body string) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, u, rdr)
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, u, err)
	}
	return res
}

// apiDoJSON is apiDo + status assertion + JSON decode.
func apiDoJSON(t *testing.T, method, u, token, body string, wantStatus int, into any) {
	t.Helper()
	res := apiDo(t, method, u, token, body)
	defer res.Body.Close()
	if res.StatusCode != wantStatus {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("%s %s = %d, want %d; body=%s", method, u, res.StatusCode, wantStatus, b)
	}
	if err := json.NewDecoder(res.Body).Decode(into); err != nil {
		t.Fatalf("decode %s %s: %v", method, u, err)
	}
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
