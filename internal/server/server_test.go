package server_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/asilluron/pixelgo/internal/config"
	"github.com/asilluron/pixelgo/internal/models"
	"github.com/asilluron/pixelgo/internal/server"
	"github.com/asilluron/pixelgo/internal/store"
	"github.com/asilluron/pixelgo/web"
)

// stubAuth is a no-op AuthStore used by the HTTP-level tests. None of these
// tests exercise signup / login / invite flows, so every method either returns
// a zero value or ErrNotFound. Ping is implemented so /healthz stays green.
type stubAuth struct{}

func (stubAuth) CreateOrg(context.Context, models.Org) error { return nil }
func (stubAuth) GetOrg(context.Context, string) (models.Org, error) {
	return models.Org{}, store.ErrNotFound
}
func (stubAuth) ListOrgs(context.Context) ([]models.Org, error)               { return nil, nil }
func (stubAuth) UpdateOrgProfile(context.Context, models.Org) error           { return nil }
func (stubAuth) AddMember(context.Context, string, string, models.Role) error { return nil }
func (stubAuth) GetMembershipForUser(context.Context, string) (string, models.Role, error) {
	return "", "", store.ErrNotFound
}
func (stubAuth) UpsertProfile(context.Context, string, bool) error  { return nil }
func (stubAuth) IsSuperAdmin(context.Context, string) (bool, error) { return false, nil }
func (stubAuth) CountSuperAdmins(context.Context) (int64, error)    { return 0, nil }
func (stubAuth) CreateInvite(context.Context, models.Invite) error  { return nil }
func (stubAuth) GetInviteByToken(context.Context, string) (models.Invite, error) {
	return models.Invite{}, store.ErrNotFound
}
func (stubAuth) ListInvitesByOrg(context.Context, string) ([]models.Invite, error) {
	return nil, nil
}
func (stubAuth) MarkInviteAccepted(context.Context, string, time.Time) error { return nil }
func (stubAuth) CreateAPIKey(context.Context, models.APIKey, string) error   { return nil }
func (stubAuth) ListAPIKeyCandidates(context.Context, string) ([]store.APIKeyRecord, error) {
	return nil, nil
}
func (stubAuth) ListAPIKeysForUser(context.Context, string) ([]models.APIKey, error) {
	return nil, nil
}
func (stubAuth) ListAPIKeysForOrg(context.Context, string) ([]models.APIKey, error) {
	return nil, nil
}
func (stubAuth) TouchAPIKey(context.Context, string, time.Time) error  { return nil }
func (stubAuth) RevokeAPIKey(context.Context, string, time.Time) error { return nil }
func (stubAuth) Ping(context.Context) error                            { return nil }
func (stubAuth) Close() error                                          { return nil }

// newTestServer spins up a miniredis-backed Server with an optional config
// mutator. The AuthStore is stubbed and supaauth.Client / Verifier are nil —
// anonymous requests never reach code paths that dereference them.
func newTestServer(t *testing.T, tweak func(*config.Config)) (*httptest.Server, *store.RedisStore, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rs, err := store.NewRedis(context.Background(), "redis://"+mr.Addr())
	if err != nil {
		t.Fatalf("NewRedis: %v", err)
	}
	t.Cleanup(func() { _ = rs.Close() })

	cfg := &config.Config{
		Addr:              ":0",
		Env:               "test",
		SessionSecret:     "test-secret",
		SessionTTL:        time.Hour,
		RateLimitPerSec:   1000,
		RateLimitBurst:    1000,
		RateLimitDisabled: true,
	}
	if tweak != nil {
		tweak(cfg)
	}

	srv, err := server.New(cfg, rs, stubAuth{}, nil, nil, web.Templates())
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, rs, mr
}

func TestHandlePixelServesGIF(t *testing.T) {
	ts, rs, _ := newTestServer(t, nil)

	res, err := http.Get(ts.URL + "/p/abc")
	if err != nil {
		t.Fatalf("GET /p/abc: %v", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "image/gif" {
		t.Fatalf("Content-Type = %q, want image/gif", ct)
	}
	if len(body) != 43 {
		t.Fatalf("len(body) = %d, want 43", len(body))
	}
	if body[0] != 'G' || body[1] != 'I' || body[2] != 'F' {
		t.Fatalf("body missing GIF magic: %x", body[:3])
	}
	if cc := res.Header.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Fatalf("Cache-Control = %q, want no-store", cc)
	}

	// The increment is fired in a goroutine; poll briefly for it to land.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		n, err := rs.GetPixelCount(context.Background(), "abc")
		if err == nil && n == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("counter for abc never reached 1")
}

func TestPixelRateLimit429(t *testing.T) {
	ts, _, _ := newTestServer(t, func(c *config.Config) {
		c.RateLimitDisabled = false
		c.RateLimitPerSec = 1
		c.RateLimitBurst = 1
	})

	// First request consumes the only token; subsequent ones within the same
	// second should be denied with 429.
	if res, err := http.Get(ts.URL + "/p/x"); err != nil || res.StatusCode != 200 {
		t.Fatalf("first req: status=%v err=%v", res.StatusCode, err)
	}
	got429 := false
	for i := 0; i < 5; i++ {
		res, err := http.Get(ts.URL + "/p/x")
		if err != nil {
			t.Fatalf("follow-up req: %v", err)
		}
		_ = res.Body.Close()
		if res.StatusCode == http.StatusTooManyRequests {
			got429 = true
			break
		}
	}
	if !got429 {
		t.Fatalf("expected a 429 within 5 follow-up requests")
	}
}

// TestRequireSessionRedirectsAnon verifies the session middleware bounces
// unauthenticated traffic on /admin to /login. End-to-end login is covered
// by Supabase (exercised manually) — we no longer own the password path.
func TestRequireSessionRedirectsAnon(t *testing.T) {
	ts, _, _ := newTestServer(t, nil)

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	res, err := client.Get(ts.URL + "/admin")
	if err != nil {
		t.Fatalf("GET /admin anon: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusFound || !strings.Contains(res.Header.Get("Location"), "/login") {
		t.Fatalf("anon /admin = %d %q, want 302 -> /login", res.StatusCode, res.Header.Get("Location"))
	}
}

func TestHealthz(t *testing.T) {
	ts, _, _ := newTestServer(t, nil)
	res, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
}

// TestLandingPageSmoke verifies GET / serves the marketing landing page
// rather than redirecting (regression guard for the prior /signup redirect),
// and that the SEO-critical bits — <title>, OG image, and JSON-LD — are
// present in the rendered output.
func TestLandingPageSmoke(t *testing.T) {
	ts, _, _ := newTestServer(t, nil)

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	res, err := client.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (no redirect)", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	got := string(body)

	wantSubs := []string{
		"<title>pixelgo",
		`property="og:image"`,
		`/static/og.png`,
		`application/ld+json`,
		`"@type": "SoftwareApplication"`,
		`href="/signup"`,
	}
	for _, s := range wantSubs {
		if !strings.Contains(got, s) {
			t.Errorf("body missing %q", s)
		}
	}
}

// TestStaticOGImage verifies the embedded /static/og.png is served and has
// the PNG magic header, so social crawlers actually get an image.
func TestStaticOGImage(t *testing.T) {
	ts, _, _ := newTestServer(t, nil)

	res, err := http.Get(ts.URL + "/static/og.png")
	if err != nil {
		t.Fatalf("GET /static/og.png: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if len(body) < 8 || string(body[1:4]) != "PNG" {
		t.Fatalf("response missing PNG magic; first bytes = %x", body[:min(8, len(body))])
	}
}
