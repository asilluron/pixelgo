// Package supaauth is a thin client for Supabase Auth (GoTrue) covering the
// subset pixelgo needs: signup, password-grant login, logout, and
// admin-create-user for bootstrap.
package supaauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client talks to a Supabase project's /auth/v1 endpoints.
type Client struct {
	BaseURL        string // https://<ref>.supabase.co
	AnonKey        string
	ServiceRoleKey string
	HTTP           *http.Client
}

// New returns a Client with a sane default HTTP timeout.
func New(baseURL, anonKey, serviceRoleKey string) *Client {
	return &Client{
		BaseURL:        strings.TrimRight(baseURL, "/"),
		AnonKey:        anonKey,
		ServiceRoleKey: serviceRoleKey,
		HTTP:           &http.Client{Timeout: 10 * time.Second},
	}
}

// Session is the subset of the GoTrue session response pixelgo cares about.
type Session struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	User         User   `json:"user"`
}

// User is the GoTrue user record (minimum needed fields).
type User struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

// AuthError is returned for non-2xx responses from GoTrue.
type AuthError struct {
	Status  int    `json:"-"`
	Code    string `json:"error_code,omitempty"`
	Message string `json:"msg"`
	ErrType string `json:"error"`
}

func (e *AuthError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("supaauth: %d %s", e.Status, e.Message)
	}
	return fmt.Sprintf("supaauth: %d %s", e.Status, e.ErrType)
}

// ErrUnauthorized surfaces 401s so callers can distinguish "bad credentials"
// from transport/5xx issues.
var ErrUnauthorized = errors.New("supaauth: unauthorized")

// Signup creates a password-based user. With mailer_autoconfirm=true on the
// project, this returns a Session directly; otherwise the session is empty
// and the user must confirm via email first.
func (c *Client) Signup(ctx context.Context, email, password string) (*Session, error) {
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	return c.doSession(ctx, "POST", "/auth/v1/signup", c.AnonKey, body)
}

// LoginPassword performs the password grant and returns a Session.
func (c *Client) LoginPassword(ctx context.Context, email, password string) (*Session, error) {
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	return c.doSession(ctx, "POST", "/auth/v1/token?grant_type=password", c.AnonKey, body)
}

// RefreshSession exchanges a refresh token for a fresh access+refresh pair.
func (c *Client) RefreshSession(ctx context.Context, refreshToken string) (*Session, error) {
	body, _ := json.Marshal(map[string]string{"refresh_token": refreshToken})
	return c.doSession(ctx, "POST", "/auth/v1/token?grant_type=refresh_token", c.AnonKey, body)
}

// Logout invalidates the given access token server-side. Best-effort; the
// caller should also clear the client cookie regardless of the result.
func (c *Client) Logout(ctx context.Context, accessToken string) error {
	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/auth/v1/logout", nil)
	if err != nil {
		return err
	}
	req.Header.Set("apikey", c.AnonKey)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	return nil
}

// AdminDeleteUser removes a user via the admin API (service role key). Used
// by integration tests to clean up fixtures; safe to call on a non-existent
// ID (returns a non-nil error that callers typically ignore).
func (c *Client) AdminDeleteUser(ctx context.Context, userID string) error {
	if c.ServiceRoleKey == "" {
		return errors.New("supaauth: service role key not configured")
	}
	req, err := http.NewRequestWithContext(ctx, "DELETE", c.BaseURL+"/auth/v1/admin/users/"+userID, nil)
	if err != nil {
		return err
	}
	req.Header.Set("apikey", c.ServiceRoleKey)
	req.Header.Set("Authorization", "Bearer "+c.ServiceRoleKey)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("supaauth: admin delete user: status %d", res.StatusCode)
	}
	return nil
}

// AdminCreateUser provisions a user via the admin API (service role key).
// The user is created already confirmed. Used for the bootstrap super-admin.
func (c *Client) AdminCreateUser(ctx context.Context, email, password string) (*User, error) {
	if c.ServiceRoleKey == "" {
		return nil, errors.New("supaauth: service role key not configured")
	}
	body, _ := json.Marshal(map[string]any{
		"email":         email,
		"password":      password,
		"email_confirm": true,
	})
	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/auth/v1/admin/users", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", c.ServiceRoleKey)
	req.Header.Set("Authorization", "Bearer "+c.ServiceRoleKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return nil, decodeErr(res)
	}
	var u User
	if err := json.NewDecoder(res.Body).Decode(&u); err != nil {
		return nil, err
	}
	return &u, nil
}

func (c *Client) doSession(ctx context.Context, method, path, apikey string, body []byte) (*Session, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", apikey)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized {
		return nil, ErrUnauthorized
	}
	if res.StatusCode/100 != 2 {
		return nil, decodeErr(res)
	}
	var s Session
	if err := json.NewDecoder(res.Body).Decode(&s); err != nil {
		return nil, err
	}
	return &s, nil
}

func decodeErr(res *http.Response) error {
	var ae AuthError
	_ = json.NewDecoder(res.Body).Decode(&ae)
	ae.Status = res.StatusCode
	return &ae
}
