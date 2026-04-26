package supaauth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

// Verifier validates Supabase access-token JWTs using the project's JWKS
// endpoint (https://<ref>.supabase.co/auth/v1/.well-known/jwks.json). It
// refreshes keys in the background so key rotation is handled for free.
type Verifier struct {
	kf keyfunc.Keyfunc
}

// NewVerifier primes the JWKS cache. Call once at startup.
func NewVerifier(ctx context.Context, baseURL string) (*Verifier, error) {
	url := baseURL + "/auth/v1/.well-known/jwks.json"
	kf, err := keyfunc.NewDefaultCtx(ctx, []string{url})
	if err != nil {
		return nil, fmt.Errorf("supaauth: init jwks: %w", err)
	}
	return &Verifier{kf: kf}, nil
}

// Claims is the subset of Supabase access-token claims pixelgo uses.
type Claims struct {
	Email string `json:"email"`
	Role  string `json:"role"`
	jwt.RegisteredClaims
}

// Verify parses and validates an access token, returning the claims.
func (v *Verifier) Verify(token string) (*Claims, error) {
	var c Claims
	t, err := jwt.ParseWithClaims(token, &c, v.kf.Keyfunc,
		jwt.WithLeeway(30*time.Second),
		jwt.WithValidMethods([]string{"ES256", "HS256"}),
	)
	if err != nil {
		return nil, err
	}
	if !t.Valid {
		return nil, errors.New("supaauth: invalid token")
	}
	return &c, nil
}
