package api

import (
	"strings"
	"testing"
)

// TestNewTokenRoundtrip: a freshly minted token must parse back to the same
// kind + prefix, its hash must validate against the plaintext, and a
// different random token must not validate against it.
func TestNewTokenRoundtrip(t *testing.T) {
	for _, k := range []Kind{KindPersonal, KindOrg} {
		t.Run(string(k), func(t *testing.T) {
			tok, prefix, hash, err := NewToken(k)
			if err != nil {
				t.Fatalf("NewToken: %v", err)
			}
			if !strings.HasPrefix(tok, "pxg_"+string(k)+"_") {
				t.Fatalf("unexpected token shape: %q", tok)
			}
			gotKind, gotPrefix, err := ParseToken(tok)
			if err != nil {
				t.Fatalf("ParseToken: %v", err)
			}
			if gotKind != k {
				t.Fatalf("kind: got %q want %q", gotKind, k)
			}
			if gotPrefix != prefix {
				t.Fatalf("prefix mismatch: got %q want %q", gotPrefix, prefix)
			}
			if err := CompareHash(hash, tok); err != nil {
				t.Fatalf("CompareHash rejected own token: %v", err)
			}

			other, _, _, err := NewToken(k)
			if err != nil {
				t.Fatalf("NewToken other: %v", err)
			}
			if err := CompareHash(hash, other); err == nil {
				t.Fatalf("CompareHash accepted foreign token")
			}
		})
	}
}

// TestParseTokenErrors covers the structural validation done without I/O.
func TestParseTokenErrors(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"wrong_vendor", "abc_pk_abcdefghijkl"},
		{"wrong_kind", "pxg_xx_abcdefghijkl"},
		{"short_payload", "pxg_pk_abc"},
		{"missing_payload", "pxg_pk_"},
		{"no_underscores", "pxgpkabcdefghijkl"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := ParseToken(tc.in); err == nil {
				t.Fatalf("expected error for %q", tc.in)
			}
		})
	}
}

// TestTokenPrefixStability: parse-time prefix must match the prefix returned
// by NewToken — the lookup depends on this invariant.
func TestTokenPrefixStability(t *testing.T) {
	tok, prefix, _, err := NewToken(KindPersonal)
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	_, gotPrefix, err := ParseToken(tok)
	if err != nil {
		t.Fatalf("ParseToken: %v", err)
	}
	if gotPrefix != prefix {
		t.Fatalf("prefix drift: mint=%q parse=%q", prefix, gotPrefix)
	}
}
