package store_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/asilluron/pixelgo/internal/models"
	"github.com/asilluron/pixelgo/internal/store"
)

// seedPixels creates n pixels for org with deterministic names, creation
// times (1 minute apart, oldest first), and alternating tags.
func seedPixels(t *testing.T, s *store.RedisStore, org string, n int) []models.Pixel {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	out := make([]models.Pixel, n)
	for i := 0; i < n; i++ {
		p := models.Pixel{
			ID:        fmt.Sprintf("px-%02d", i),
			OrgID:     org,
			Name:      fmt.Sprintf("Pixel %02d", i),
			URL:       fmt.Sprintf("https://shop.example.com/products/%d", i),
			Tags:      []string{"all"},
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
		}
		if i%2 == 0 {
			p.Tags = append(p.Tags, "even")
		}
		if err := s.CreatePixel(ctx, p); err != nil {
			t.Fatalf("CreatePixel %s: %v", p.ID, err)
		}
		out[i] = p
	}
	return out
}

func ids(pixels []models.Pixel) []string {
	out := make([]string, len(pixels))
	for i, p := range pixels {
		out[i] = p.ID
	}
	return out
}

func TestListPixelsSortAndPaginate(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	seedPixels(t, s, "org1", 5)

	// Default: newest first, paginated.
	page, err := s.ListPixels(ctx, store.PixelListOptions{OrgID: "org1", Limit: 2})
	if err != nil {
		t.Fatalf("ListPixels: %v", err)
	}
	if page.Total != 5 {
		t.Fatalf("total = %d, want 5", page.Total)
	}
	if got := ids(page.Pixels); len(got) != 2 || got[0] != "px-04" || got[1] != "px-03" {
		t.Fatalf("page 1 = %v, want [px-04 px-03]", got)
	}

	// Second page via offset.
	page, err = s.ListPixels(ctx, store.PixelListOptions{OrgID: "org1", Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("ListPixels offset: %v", err)
	}
	if got := ids(page.Pixels); len(got) != 2 || got[0] != "px-02" || got[1] != "px-01" {
		t.Fatalf("page 2 = %v, want [px-02 px-01]", got)
	}

	// Oldest first.
	page, err = s.ListPixels(ctx, store.PixelListOptions{OrgID: "org1", Sort: store.SortCreatedAsc, Limit: 2})
	if err != nil {
		t.Fatalf("ListPixels asc: %v", err)
	}
	if got := ids(page.Pixels); got[0] != "px-00" || got[1] != "px-01" {
		t.Fatalf("asc page = %v, want [px-00 px-01]", got)
	}

	// Name sorts (case-insensitive lex index).
	page, err = s.ListPixels(ctx, store.PixelListOptions{OrgID: "org1", Sort: store.SortNameDesc, Limit: 1})
	if err != nil {
		t.Fatalf("ListPixels name desc: %v", err)
	}
	if got := ids(page.Pixels); got[0] != "px-04" {
		t.Fatalf("name desc = %v, want [px-04]", got)
	}
}

func TestListPixelsFilters(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	seedPixels(t, s, "org1", 5)
	// A pixel in another org must never leak in.
	other := models.Pixel{ID: "foreign", OrgID: "org2", Name: "Pixel 99", CreatedAt: time.Now().UTC()}
	if err := s.CreatePixel(ctx, other); err != nil {
		t.Fatalf("CreatePixel foreign: %v", err)
	}

	// Tag filter, created-desc within the tag.
	page, err := s.ListPixels(ctx, store.PixelListOptions{OrgID: "org1", Tag: "even", Limit: 10})
	if err != nil {
		t.Fatalf("ListPixels tag: %v", err)
	}
	if page.Total != 3 {
		t.Fatalf("tag total = %d, want 3", page.Total)
	}
	if got := ids(page.Pixels); got[0] != "px-04" || got[1] != "px-02" || got[2] != "px-00" {
		t.Fatalf("tag page = %v, want [px-04 px-02 px-00]", got)
	}

	// Case-insensitive name prefix.
	page, err = s.ListPixels(ctx, store.PixelListOptions{OrgID: "org1", NamePrefix: "pIxEl 0", Limit: 10})
	if err != nil {
		t.Fatalf("ListPixels prefix: %v", err)
	}
	if page.Total != 5 {
		t.Fatalf("prefix total = %d, want 5", page.Total)
	}
	page, err = s.ListPixels(ctx, store.PixelListOptions{OrgID: "org1", NamePrefix: "pixel 03", Limit: 10})
	if err != nil {
		t.Fatalf("ListPixels narrow prefix: %v", err)
	}
	if page.Total != 1 || page.Pixels[0].ID != "px-03" {
		t.Fatalf("narrow prefix = %+v, want just px-03", ids(page.Pixels))
	}

	// No match.
	page, err = s.ListPixels(ctx, store.PixelListOptions{OrgID: "org1", NamePrefix: "zzz", Limit: 10})
	if err != nil {
		t.Fatalf("ListPixels no match: %v", err)
	}
	if page.Total != 0 || len(page.Pixels) != 0 {
		t.Fatalf("no-match page = %+v", page)
	}
}

func TestSoftDeleteLifecycle(t *testing.T) {
	s, mr := newTestStore(t)
	ctx := context.Background()
	pixels := seedPixels(t, s, "org1", 3)
	target := pixels[1]

	// A few hits before deletion.
	now := time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if _, err := s.IncrPixel(ctx, target.ID, now); err != nil {
			t.Fatalf("IncrPixel: %v", err)
		}
	}

	deletedAt := now.Add(time.Hour)
	p, err := s.SoftDeletePixel(ctx, target.ID, deletedAt)
	if err != nil {
		t.Fatalf("SoftDeletePixel: %v", err)
	}
	if p.DeletedAt == nil || !p.DeletedAt.Equal(deletedAt) {
		t.Fatalf("DeletedAt = %v, want %v", p.DeletedAt, deletedAt)
	}
	if got := p.PurgeAt(); got == nil || !got.Equal(deletedAt.Add(models.PixelDeleteRetention)) {
		t.Fatalf("PurgeAt = %v, want deletedAt+30d", got)
	}

	// Idempotent: second delete returns the same state.
	again, err := s.SoftDeletePixel(ctx, target.ID, deletedAt.Add(time.Hour))
	if err != nil {
		t.Fatalf("second SoftDeletePixel: %v", err)
	}
	if !again.DeletedAt.Equal(deletedAt) {
		t.Fatalf("second delete moved DeletedAt to %v", again.DeletedAt)
	}

	// Unknown id -> ErrNotFound.
	if _, err := s.SoftDeletePixel(ctx, "nope", deletedAt); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete unknown = %v, want ErrNotFound", err)
	}

	// Gone from live listings, present in deleted listing.
	page, err := s.ListPixels(ctx, store.PixelListOptions{OrgID: "org1", Limit: 10})
	if err != nil {
		t.Fatalf("ListPixels: %v", err)
	}
	if page.Total != 2 {
		t.Fatalf("live total = %d, want 2", page.Total)
	}
	for _, lp := range page.Pixels {
		if lp.ID == target.ID {
			t.Fatalf("deleted pixel still in live list")
		}
	}
	del, err := s.ListPixels(ctx, store.PixelListOptions{OrgID: "org1", Status: store.StatusDeleted, Limit: 10})
	if err != nil {
		t.Fatalf("ListPixels deleted: %v", err)
	}
	if del.Total != 1 || del.Pixels[0].ID != target.ID {
		t.Fatalf("deleted list = %v, want [%s]", ids(del.Pixels), target.ID)
	}

	// Gone from tag and name indexes too.
	tagged, err := s.ListPixels(ctx, store.PixelListOptions{OrgID: "org1", Tag: "all", Limit: 10})
	if err != nil {
		t.Fatalf("ListPixels tag: %v", err)
	}
	if tagged.Total != 2 {
		t.Fatalf("tag total after delete = %d, want 2", tagged.Total)
	}

	// Hot path stops counting: counter stays at 3.
	if n, err := s.IncrPixel(ctx, target.ID, deletedAt.Add(time.Minute)); err != nil || n != 0 {
		t.Fatalf("IncrPixel on deleted = (%d, %v), want (0, nil)", n, err)
	}
	if n, _ := s.GetPixelCount(ctx, target.ID); n != 3 {
		t.Fatalf("count after deleted incr = %d, want 3", n)
	}

	// Expunge before the retention window: nothing happens.
	if n, err := s.ExpungeDuePixels(ctx, deletedAt.Add(29*24*time.Hour)); err != nil || n != 0 {
		t.Fatalf("early expunge = (%d, %v), want (0, nil)", n, err)
	}
	if _, err := s.GetPixel(ctx, target.ID); err != nil {
		t.Fatalf("pixel gone before retention elapsed: %v", err)
	}

	// After 30 days everything is expunged: metadata, counters, indexes.
	expungeAt := deletedAt.Add(31 * 24 * time.Hour)
	n, err := s.ExpungeDuePixels(ctx, expungeAt)
	if err != nil || n != 1 {
		t.Fatalf("expunge = (%d, %v), want (1, nil)", n, err)
	}
	if _, err := s.GetPixel(ctx, target.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetPixel after expunge = %v, want ErrNotFound", err)
	}
	if mr.Exists("pixel:count:" + target.ID) {
		t.Fatalf("lifetime counter survived expunge")
	}
	del, err = s.ListPixels(ctx, store.PixelListOptions{OrgID: "org1", Status: store.StatusDeleted, Limit: 10})
	if err != nil {
		t.Fatalf("ListPixels deleted after expunge: %v", err)
	}
	if del.Total != 0 {
		t.Fatalf("deleted list total after expunge = %d, want 0", del.Total)
	}
	// Expunge is drained; a second run is a no-op.
	if n, err := s.ExpungeDuePixels(ctx, expungeAt); err != nil || n != 0 {
		t.Fatalf("second expunge = (%d, %v), want (0, nil)", n, err)
	}
}

// TestReindexPixels simulates pixels created before the catalog indexes
// existed (bare SET/JSON writes) and verifies the boot-time reindex makes
// them visible to ListPixels exactly once.
func TestReindexPixels(t *testing.T) {
	s, mr := newTestStore(t)
	ctx := context.Background()

	// Legacy layout: JSON + membership sets only.
	legacy := models.Pixel{
		ID: "old-1", OrgID: "org1", Name: "Legacy",
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	blob := `{"id":"old-1","org_id":"org1","name":"Legacy","created_at":"2026-01-01T00:00:00Z"}`
	mr.Set("pixel:"+legacy.ID, blob)
	mr.SAdd("pixels", legacy.ID)
	mr.SAdd("pixel:org:org1", legacy.ID)

	// Invisible to the catalog before reindex.
	page, err := s.ListPixels(ctx, store.PixelListOptions{OrgID: "org1", Limit: 10})
	if err != nil {
		t.Fatalf("ListPixels: %v", err)
	}
	if page.Total != 0 {
		t.Fatalf("pre-reindex total = %d, want 0", page.Total)
	}

	if err := s.ReindexPixels(ctx); err != nil {
		t.Fatalf("ReindexPixels: %v", err)
	}
	page, err = s.ListPixels(ctx, store.PixelListOptions{OrgID: "org1", Limit: 10})
	if err != nil {
		t.Fatalf("ListPixels after reindex: %v", err)
	}
	if page.Total != 1 || page.Pixels[0].ID != "old-1" {
		t.Fatalf("post-reindex page = %v, want [old-1]", ids(page.Pixels))
	}

	// Versioned: second call is a no-op (and must not error).
	if err := s.ReindexPixels(ctx); err != nil {
		t.Fatalf("second ReindexPixels: %v", err)
	}
}

func TestGetPixelsBulkSeries(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		if _, err := s.IncrPixel(ctx, "a", base); err != nil {
			t.Fatalf("IncrPixel: %v", err)
		}
	}
	if _, err := s.IncrPixel(ctx, "b", base.Add(-24*time.Hour)); err != nil {
		t.Fatalf("IncrPixel b: %v", err)
	}

	daily, err := s.GetPixelsDaily(ctx, []string{"a", "b"}, 2, base)
	if err != nil {
		t.Fatalf("GetPixelsDaily: %v", err)
	}
	if got := daily["a"]; len(got) != 2 || got[0].Count != 0 || got[1].Count != 4 {
		t.Fatalf("daily[a] = %+v, want [0 4]", got)
	}
	if got := daily["b"]; got[0].Count != 1 || got[1].Count != 0 {
		t.Fatalf("daily[b] = %+v, want [1 0]", got)
	}

	hourly, err := s.GetPixelsHourly(ctx, []string{"a"}, 3, base)
	if err != nil {
		t.Fatalf("GetPixelsHourly: %v", err)
	}
	if got := hourly["a"]; len(got) != 3 || got[2].Count != 4 {
		t.Fatalf("hourly[a] = %+v, want last bucket 4", got)
	}
}
