package api

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// Token format: pxg_<kind>_<24-byte base64>
//
//   - "pxg"     → vendor/namespace, lets operators grep logs for leaked keys.
//   - kind      → "pk" (personal) or "ok" (org). Cheap hint; the DB row is
//                 the authority on type.
//   - payload   → 24 random bytes, base64-url without padding (32 chars).
//
// Example: pxg_pk_yN7qv-abc0xYZ123...
//
// `prefix` (stored in plaintext for lookup) is the full string up to and
// including the payload's first prefixChars characters. 12 chars of base64
// over random bytes gives ~72 bits of entropy before bcrypt compare, which
// keeps the candidate list at 0–1 rows in practice.
const (
	tokenVendor    = "pxg"
	kindPersonal   = "pk"
	kindOrg        = "ok"
	payloadBytes   = 24
	prefixChars    = 12
	bcryptCost     = bcrypt.DefaultCost
	tokenSeparator = "_"
)

// ErrInvalidToken is returned when a bearer token is malformed.
var ErrInvalidToken = errors.New("api: invalid token")

// NewToken mints a fresh plaintext token for the given key type and returns
// the full token (to be shown to the caller once) along with its stable
// prefix and bcrypt hash (both safe to persist).
func NewToken(kind Kind) (token, prefix, hash string, err error) {
	var buf [payloadBytes]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", "", "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(buf[:])
	token = tokenVendor + tokenSeparator + string(kind) + tokenSeparator + payload

	h, err := bcrypt.GenerateFromPassword([]byte(token), bcryptCost)
	if err != nil {
		return "", "", "", err
	}
	return token, tokenPrefix(token), string(h), nil
}

// Kind is the runtime tag embedded in the token. It mirrors models.APIKeyType
// but kept local so this file has no dependency on the models package.
type Kind string

const (
	KindPersonal Kind = kindPersonal
	KindOrg      Kind = kindOrg
)

// ParseToken splits a raw bearer value into its kind and prefix without
// touching the database. A malformed token returns ErrInvalidToken.
func ParseToken(raw string) (kind Kind, prefix string, err error) {
	raw = strings.TrimSpace(raw)
	parts := strings.SplitN(raw, tokenSeparator, 3)
	if len(parts) != 3 || parts[0] != tokenVendor || len(parts[2]) < prefixChars {
		return "", "", ErrInvalidToken
	}
	k := Kind(parts[1])
	if k != KindPersonal && k != KindOrg {
		return "", "", ErrInvalidToken
	}
	return k, tokenPrefix(raw), nil
}

// CompareHash is bcrypt.CompareHashAndPassword with a friendlier name.
// Returns nil on match, a non-nil error otherwise.
func CompareHash(hash, token string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(token))
}

// tokenPrefix extracts the stable lookup prefix from a full token.
func tokenPrefix(tok string) string {
	// vendor_kind_ + first prefixChars of payload
	head := tokenVendor + tokenSeparator // "pxg_"
	if !strings.HasPrefix(tok, head) {
		return ""
	}
	rest := tok[len(head):]
	sep := strings.IndexByte(rest, tokenSeparator[0])
	if sep < 0 || len(rest) < sep+1+prefixChars {
		return ""
	}
	return tok[:len(head)+sep+1+prefixChars]
}
