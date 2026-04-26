//go:build integration

package server_test

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// mustClient returns an authenticated http.Client with a populated cookie
// jar after performing a /login POST against Supabase.
func mustClient(t *testing.T, ts *httptest.Server, email, password, _ string) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	postForm(t, client, ts.URL+"/login",
		url.Values{"email": {email}, "password": {password}}, "/admin")
	return client
}

// signup drives POST /signup (optionally with an invite token) and returns
// the Supabase user ID of the newly-created account. Looks the ID up via
// the pool since /signup only sets cookies, not a visible identifier.
func signup(t *testing.T, ts *httptest.Server, pool *pgxpool.Pool, email, password, inviteToken string) string {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	form := url.Values{"email": {email}, "password": {password}}
	wantLoc := "/signup/org"
	if inviteToken != "" {
		form.Set("invite", inviteToken)
		wantLoc = "/admin"
	}
	postForm(t, client, ts.URL+"/signup", form, wantLoc)
	return lookupUserID(t, pool, email)
}

// postForm POSTs form-encoded values and asserts a 302 to wantLocation.
func postForm(t *testing.T, client *http.Client, u string, values url.Values, wantLocation string) {
	t.Helper()
	res, err := client.PostForm(u, values)
	if err != nil {
		t.Fatalf("POST %s: %v", u, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusFound {
		t.Fatalf("POST %s status = %d, want 302", u, res.StatusCode)
	}
	if loc := res.Header.Get("Location"); loc != wantLocation {
		t.Fatalf("POST %s Location = %q, want %q", u, loc, wantLocation)
	}
}

// mustGet GETs a URL with redirects followed and returns the response. The
// caller's CheckRedirect is restored before this function returns so later
// POSTs still see the no-follow policy set by mustClient.
func mustGet(t *testing.T, client *http.Client, u string) *http.Response {
	t.Helper()
	prev := client.CheckRedirect
	client.CheckRedirect = nil
	defer func() { client.CheckRedirect = prev }()
	res, err := client.Get(u)
	if err != nil {
		t.Fatalf("GET %s: %v", u, err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	return res
}

// lookupUserID finds a just-created Supabase user by email. Used by signup()
// because our /signup handler doesn't echo the user ID back to the client.
func lookupUserID(t *testing.T, pool *pgxpool.Pool, email string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var id string
	if err := pool.QueryRow(ctx,
		`select id::text from auth.users where email = $1`, email,
	).Scan(&id); err != nil {
		t.Fatalf("lookup user %q: %v", email, err)
	}
	return id
}
