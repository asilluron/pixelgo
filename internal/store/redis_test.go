package store_test

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/asilluron/pixelgo/internal/store"
)

// newTestStore spins up an in-memory miniredis and returns a connected
// RedisStore plus the miniredis handle (for TTL / clock manipulation).
// Auth data (orgs, users, invites) lives in Postgres now; this helper
// exists purely for the pixel-counter tests in counters_test.go.
func newTestStore(t *testing.T) (*store.RedisStore, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	s, err := store.NewRedis(context.Background(), "redis://"+mr.Addr())
	if err != nil {
		t.Fatalf("NewRedis: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, mr
}
