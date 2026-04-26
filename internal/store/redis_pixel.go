package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/asilluron/pixelgo/internal/models"
	"github.com/redis/go-redis/v9"
)

// --- Pixels ---

func (r *RedisStore) CreatePixel(ctx context.Context, p models.Pixel) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	pipe := r.c.TxPipeline()
	pipe.SetNX(ctx, kPixel+p.ID, b, 0)
	pipe.SAdd(ctx, kPixels, p.ID)
	pipe.SAdd(ctx, kPixelOrg+p.OrgID, p.ID)
	_, err = pipe.Exec(ctx)
	return err
}

func (r *RedisStore) GetPixel(ctx context.Context, id string) (models.Pixel, error) {
	var p models.Pixel
	b, err := r.c.Get(ctx, kPixel+id).Bytes()
	if err == redis.Nil {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	return p, json.Unmarshal(b, &p)
}

func (r *RedisStore) ListPixelsByOrg(ctx context.Context, orgID string) ([]models.Pixel, error) {
	ids, err := r.c.SMembers(ctx, kPixelOrg+orgID).Result()
	if err != nil {
		return nil, err
	}
	return fetchJSONList[models.Pixel](ctx, r.c, kPixel, ids)
}

func (r *RedisStore) ListAllPixels(ctx context.Context) ([]models.Pixel, error) {
	ids, err := r.c.SMembers(ctx, kPixels).Result()
	if err != nil {
		return nil, err
	}
	return fetchJSONList[models.Pixel](ctx, r.c, kPixel, ids)
}

// --- Counters (hot path) ---

// dayLabel formats t as "YYYYMMDD" in UTC.
func dayLabel(t time.Time) string { return t.UTC().Format("20060102") }

// hourLabel formats t as "YYYYMMDDHH" in UTC.
func hourLabel(t time.Time) string { return t.UTC().Format("2006010215") }

// IncrPixel bumps lifetime + daily + hourly buckets for pixelID, all in a
// single pipelined round-trip. Returns the new lifetime count.
func (r *RedisStore) IncrPixel(ctx context.Context, pixelID string, t time.Time) (int64, error) {
	dayKey := kPixelCountDay + pixelID + ":" + dayLabel(t)
	hourKey := kPixelCountHour + pixelID + ":" + hourLabel(t)

	pipe := r.c.Pipeline()
	life := pipe.Incr(ctx, kPixelCount+pixelID)
	pipe.Incr(ctx, dayKey)
	pipe.Expire(ctx, dayKey, time.Duration(dailyTTL)*time.Second)
	pipe.Incr(ctx, hourKey)
	pipe.Expire(ctx, hourKey, time.Duration(hourlyTTL)*time.Second)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, err
	}
	return life.Val(), nil
}

func (r *RedisStore) GetPixelCount(ctx context.Context, pixelID string) (int64, error) {
	v, err := r.c.Get(ctx, kPixelCount+pixelID).Int64()
	if err == redis.Nil {
		return 0, nil
	}
	return v, err
}

// GetPixelCounts fetches many lifetime counters in one round-trip via MGET.
func (r *RedisStore) GetPixelCounts(ctx context.Context, pixelIDs []string) (map[string]int64, error) {
	out := make(map[string]int64, len(pixelIDs))
	if len(pixelIDs) == 0 {
		return out, nil
	}
	keys := make([]string, len(pixelIDs))
	for i, id := range pixelIDs {
		keys[i] = kPixelCount + id
	}
	vals, err := r.c.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	for i, v := range vals {
		out[pixelIDs[i]] = parseMGetInt64(v)
	}
	return out, nil
}

// GetPixelBundles returns lifetime/today/hour counts for each pixel using one
// MGET. For N pixels we fetch 3N keys.
func (r *RedisStore) GetPixelBundles(ctx context.Context, pixelIDs []string, now time.Time) (map[string]CountBundle, error) {
	out := make(map[string]CountBundle, len(pixelIDs))
	if len(pixelIDs) == 0 {
		return out, nil
	}
	day := dayLabel(now)
	hour := hourLabel(now)
	keys := make([]string, 0, 3*len(pixelIDs))
	for _, id := range pixelIDs {
		keys = append(keys,
			kPixelCount+id,
			kPixelCountDay+id+":"+day,
			kPixelCountHour+id+":"+hour,
		)
	}
	vals, err := r.c.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	for i, id := range pixelIDs {
		out[id] = CountBundle{
			Total:    parseMGetInt64(vals[3*i]),
			Today:    parseMGetInt64(vals[3*i+1]),
			LastHour: parseMGetInt64(vals[3*i+2]),
		}
	}
	return out, nil
}

// GetPixelDaily returns the last `days` daily counts ending at `now` (UTC).
func (r *RedisStore) GetPixelDaily(ctx context.Context, pixelID string, days int, now time.Time) ([]BucketPoint, error) {
	if days <= 0 {
		return nil, nil
	}
	keys := make([]string, days)
	labels := make([]string, days)
	for i := 0; i < days; i++ {
		t := now.Add(time.Duration(-(days - 1 - i)) * 24 * time.Hour)
		labels[i] = dayLabel(t)
		keys[i] = kPixelCountDay + pixelID + ":" + labels[i]
	}
	vals, err := r.c.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	out := make([]BucketPoint, days)
	for i := range vals {
		out[i] = BucketPoint{Label: labels[i], Count: parseMGetInt64(vals[i])}
	}
	return out, nil
}

// GetPixelHourly returns the last `hours` hourly counts ending at `now` (UTC).
func (r *RedisStore) GetPixelHourly(ctx context.Context, pixelID string, hours int, now time.Time) ([]BucketPoint, error) {
	if hours <= 0 {
		return nil, nil
	}
	keys := make([]string, hours)
	labels := make([]string, hours)
	for i := 0; i < hours; i++ {
		t := now.Add(time.Duration(-(hours - 1 - i)) * time.Hour)
		labels[i] = hourLabel(t)
		keys[i] = kPixelCountHour + pixelID + ":" + labels[i]
	}
	vals, err := r.c.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	out := make([]BucketPoint, hours)
	for i := range vals {
		out[i] = BucketPoint{Label: labels[i], Count: parseMGetInt64(vals[i])}
	}
	return out, nil
}

// parseMGetInt64 decodes an MGET entry (string | nil) into an int64. Invalid
// values decode to 0 rather than erroring — missing/corrupt counters should
// not break the dashboard.
func parseMGetInt64(v any) int64 {
	if v == nil {
		return 0
	}
	s, _ := v.(string)
	var n int64
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0
		}
		n = n*10 + int64(ch-'0')
	}
	return n
}

// --- helpers ---

// fetchJSONList reads multiple keys (prefix+id) via MGET and decodes them.
func fetchJSONList[T any](ctx context.Context, c *redis.Client, prefix string, ids []string) ([]T, error) {
	if len(ids) == 0 {
		return []T{}, nil
	}
	keys := make([]string, len(ids))
	for i, id := range ids {
		keys[i] = prefix + id
	}
	vals, err := c.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	out := make([]T, 0, len(vals))
	for _, v := range vals {
		if v == nil {
			continue
		}
		s, _ := v.(string)
		var item T
		if err := json.Unmarshal([]byte(s), &item); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}
