package store_test

import (
	"context"
	"testing"
	"time"
)

func TestIncrPixelBucketsAndBundles(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	// Fixed anchor so hour/day boundaries are deterministic.
	base := time.Date(2026, 4, 20, 15, 30, 0, 0, time.UTC)

	// Five hits in the anchor hour.
	for i := 0; i < 5; i++ {
		if _, err := s.IncrPixel(ctx, "p1", base); err != nil {
			t.Fatalf("IncrPixel: %v", err)
		}
	}
	// Two hits in a later hour of the same UTC day.
	later := base.Add(2 * time.Hour)
	for i := 0; i < 2; i++ {
		if _, err := s.IncrPixel(ctx, "p1", later); err != nil {
			t.Fatalf("IncrPixel later: %v", err)
		}
	}
	// One hit the next day.
	nextDay := base.Add(24 * time.Hour)
	if _, err := s.IncrPixel(ctx, "p1", nextDay); err != nil {
		t.Fatalf("IncrPixel nextDay: %v", err)
	}

	// Bundles at `base`: today=7 (5+2 in same day), hour=5 (just base hour),
	// total=8.
	bundles, err := s.GetPixelBundles(ctx, []string{"p1"}, base)
	if err != nil {
		t.Fatalf("GetPixelBundles: %v", err)
	}
	got := bundles["p1"]
	if got.Total != 8 || got.Today != 7 || got.LastHour != 5 {
		t.Fatalf("bundle = %+v, want total=8 today=7 hour=5", got)
	}

	// Daily series spanning both days: oldest first.
	daily, err := s.GetPixelDaily(ctx, "p1", 2, nextDay)
	if err != nil {
		t.Fatalf("GetPixelDaily: %v", err)
	}
	if len(daily) != 2 || daily[0].Count != 7 || daily[1].Count != 1 {
		t.Fatalf("daily = %+v, want [7 1]", daily)
	}

	// Hourly series: last 3 hours ending at `later` -> [base=5, base+1h=0, later=2].
	hourly, err := s.GetPixelHourly(ctx, "p1", 3, later)
	if err != nil {
		t.Fatalf("GetPixelHourly: %v", err)
	}
	if len(hourly) != 3 || hourly[0].Count != 5 || hourly[1].Count != 0 || hourly[2].Count != 2 {
		t.Fatalf("hourly = %+v, want [5 0 2]", hourly)
	}
}

func TestGetPixelCountsBulk(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, id := range []string{"a", "a", "a", "b"} {
		if _, err := s.IncrPixel(ctx, id, now); err != nil {
			t.Fatalf("IncrPixel: %v", err)
		}
	}
	counts, err := s.GetPixelCounts(ctx, []string{"a", "b", "missing"})
	if err != nil {
		t.Fatalf("GetPixelCounts: %v", err)
	}
	if counts["a"] != 3 || counts["b"] != 1 || counts["missing"] != 0 {
		t.Fatalf("counts = %+v", counts)
	}
}

// Sanity check: IncrPixel sets TTL on bucket keys so we don't retain unbounded history.
func TestBucketTTLs(t *testing.T) {
	s, mr := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	if _, err := s.IncrPixel(ctx, "p1", now); err != nil {
		t.Fatalf("IncrPixel: %v", err)
	}
	// Lifetime must not expire.
	if ttl := mr.TTL("pixel:count:p1"); ttl != 0 {
		t.Fatalf("lifetime TTL = %v, want 0 (no expiration)", ttl)
	}
	// Hourly must expire (within 72h window).
	if ttl := mr.TTL("pixel:count:h:p1:2026042012"); ttl <= 0 || ttl > 72*time.Hour {
		t.Fatalf("hourly TTL = %v, want (0, 72h]", ttl)
	}
	// Daily must expire (within 35d window).
	if ttl := mr.TTL("pixel:count:d:p1:20260420"); ttl <= 0 || ttl > 35*24*time.Hour {
		t.Fatalf("daily TTL = %v, want (0, 35d]", ttl)
	}
}
