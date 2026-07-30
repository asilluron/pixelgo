package store

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/asilluron/pixelgo/internal/models"
	"github.com/redis/go-redis/v9"
)

// --- Pixels ---

// nameMember builds the lexicographic name-index member. The NUL separator
// sorts before every printable byte, so members order by lowercased name
// first and id second, and prefix scans never bleed across names.
func nameMember(name, id string) string {
	return strings.ToLower(name) + "\x00" + id
}

// idFromNameMember recovers the pixel id from a name-index member.
func idFromNameMember(m string) string {
	if i := strings.LastIndexByte(m, 0); i >= 0 {
		return m[i+1:]
	}
	return m
}

func (r *RedisStore) CreatePixel(ctx context.Context, p models.Pixel) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	score := float64(p.CreatedAt.Unix())
	pipe := r.c.TxPipeline()
	pipe.SetNX(ctx, kPixel+p.ID, b, 0)
	pipe.SAdd(ctx, kPixels, p.ID)
	pipe.SAdd(ctx, kPixelOrg+p.OrgID, p.ID)
	pipe.ZAdd(ctx, kOrgCreated(p.OrgID), redis.Z{Score: score, Member: p.ID})
	pipe.ZAdd(ctx, kOrgName(p.OrgID), redis.Z{Score: 0, Member: nameMember(p.Name, p.ID)})
	for _, tag := range p.Tags {
		pipe.ZAdd(ctx, kOrgTag(p.OrgID, tag), redis.Z{Score: score, Member: p.ID})
	}
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

// GetPixels bulk-fetches pixels by id in one MGET, preserving input order.
// Missing ids are silently skipped.
func (r *RedisStore) GetPixels(ctx context.Context, ids []string) ([]models.Pixel, error) {
	return fetchJSONList[models.Pixel](ctx, r.c, kPixel, ids)
}

func (r *RedisStore) ListPixelsByOrg(ctx context.Context, orgID string) ([]models.Pixel, error) {
	ids, err := r.c.SMembers(ctx, kPixelOrg+orgID).Result()
	if err != nil {
		return nil, err
	}
	return fetchJSONList[models.Pixel](ctx, r.c, kPixel, ids)
}

// ListPixelIDsByOrg returns just the live pixel ids for an org (no metadata
// MGET). Used where callers only need ids — e.g. bulk counter reads.
func (r *RedisStore) ListPixelIDsByOrg(ctx context.Context, orgID string) ([]string, error) {
	return r.c.SMembers(ctx, kPixelOrg+orgID).Result()
}

func (r *RedisStore) ListAllPixels(ctx context.Context) ([]models.Pixel, error) {
	ids, err := r.c.SMembers(ctx, kPixels).Result()
	if err != nil {
		return nil, err
	}
	return fetchJSONList[models.Pixel](ctx, r.c, kPixel, ids)
}

// ListPixels serves the catalog: index-backed sort/filter/pagination. Every
// branch is O(log N + page) against a ZSET plus one MGET for the page's
// metadata — no full-org scans, so it holds up for orgs with thousands of
// pixels. See PixelListOptions for the sort-coercion rules.
func (r *RedisStore) ListPixels(ctx context.Context, opts PixelListOptions) (PixelPage, error) {
	if opts.Limit <= 0 {
		return PixelPage{}, errors.New("store: ListPixels requires Limit > 0")
	}
	if opts.Offset < 0 {
		opts.Offset = 0
	}
	if opts.Status == "" {
		opts.Status = StatusActive
	}
	if opts.Sort == "" {
		opts.Sort = SortCreatedDesc
	}

	var (
		ids   []string
		total int64
		err   error
	)
	switch {
	case opts.Status == StatusDeleted:
		// Deleted pixels order by deletion time (score of the deleted ZSET).
		asc := opts.Sort == SortCreatedAsc || opts.Sort == SortNameAsc
		ids, total, err = r.pageByScore(ctx, kOrgDeleted(opts.OrgID), asc, opts.Offset, opts.Limit)

	case opts.NamePrefix != "":
		// Prefix search runs on the lex index, so results are name-ordered
		// (A→Z unless name_desc was requested).
		ids, total, err = r.pageByNamePrefix(ctx, kOrgName(opts.OrgID), opts.NamePrefix, opts.Sort == SortNameDesc, opts.Offset, opts.Limit)

	case opts.Tag != "":
		// Tag ZSETs are scored by created-at, so results are created-ordered.
		asc := opts.Sort == SortCreatedAsc
		ids, total, err = r.pageByScore(ctx, kOrgTag(opts.OrgID, opts.Tag), asc, opts.Offset, opts.Limit)

	case opts.Sort == SortNameAsc || opts.Sort == SortNameDesc:
		ids, total, err = r.pageByNamePrefix(ctx, kOrgName(opts.OrgID), "", opts.Sort == SortNameDesc, opts.Offset, opts.Limit)

	default:
		ids, total, err = r.pageByScore(ctx, kOrgCreated(opts.OrgID), opts.Sort == SortCreatedAsc, opts.Offset, opts.Limit)
	}
	if err != nil {
		return PixelPage{}, err
	}

	pixels, err := fetchJSONList[models.Pixel](ctx, r.c, kPixel, ids)
	if err != nil {
		return PixelPage{}, err
	}
	return PixelPage{Pixels: pixels, Total: total}, nil
}

// pageByScore pages a score-ordered ZSET (created-at or deleted-at scores).
func (r *RedisStore) pageByScore(ctx context.Context, key string, asc bool, offset, limit int) ([]string, int64, error) {
	total, err := r.c.ZCard(ctx, key).Result()
	if err != nil {
		return nil, 0, err
	}
	start, stop := int64(offset), int64(offset+limit-1)
	var ids []string
	if asc {
		ids, err = r.c.ZRange(ctx, key, start, stop).Result()
	} else {
		ids, err = r.c.ZRevRange(ctx, key, start, stop).Result()
	}
	return ids, total, err
}

// pageByNamePrefix pages the lexicographic name index, optionally restricted
// to a case-insensitive name prefix. "\xff" is a safe upper bound because
// no UTF-8 encoded name byte equals 0xFF.
func (r *RedisStore) pageByNamePrefix(ctx context.Context, key, prefix string, desc bool, offset, limit int) ([]string, int64, error) {
	min, max := "-", "+"
	if prefix != "" {
		p := strings.ToLower(prefix)
		min, max = "["+p, "["+p+"\xff"
	}
	total, err := r.c.ZLexCount(ctx, key, min, max).Result()
	if err != nil {
		return nil, 0, err
	}
	rng := &redis.ZRangeBy{Min: min, Max: max, Offset: int64(offset), Count: int64(limit)}
	var members []string
	if desc {
		members, err = r.c.ZRevRangeByLex(ctx, key, rng).Result()
	} else {
		members, err = r.c.ZRangeByLex(ctx, key, rng).Result()
	}
	if err != nil {
		return nil, 0, err
	}
	ids := make([]string, len(members))
	for i, m := range members {
		ids[i] = idFromNameMember(m)
	}
	return ids, total, nil
}

// SoftDeletePixel marks a pixel deleted, drops it from every live index (so
// listings and the hot-path counter guard take effect immediately), and
// schedules it for expunge PixelDeleteRetention later. Idempotent: deleting
// an already-deleted pixel returns its current state without rescheduling.
func (r *RedisStore) SoftDeletePixel(ctx context.Context, id string, at time.Time) (models.Pixel, error) {
	p, err := r.GetPixel(ctx, id)
	if err != nil {
		return p, err
	}
	if p.DeletedAt != nil {
		return p, nil
	}
	del := at.UTC()
	p.DeletedAt = &del
	b, err := json.Marshal(p)
	if err != nil {
		return p, err
	}
	purge := del.Add(models.PixelDeleteRetention)

	pipe := r.c.TxPipeline()
	pipe.Set(ctx, kPixel+id, b, 0)
	pipe.SRem(ctx, kPixels, id)
	pipe.SRem(ctx, kPixelOrg+p.OrgID, id)
	pipe.ZRem(ctx, kOrgCreated(p.OrgID), id)
	pipe.ZRem(ctx, kOrgName(p.OrgID), nameMember(p.Name, id))
	for _, tag := range p.Tags {
		pipe.ZRem(ctx, kOrgTag(p.OrgID, tag), id)
	}
	pipe.SAdd(ctx, kPixelsDeleted, id)
	pipe.ZAdd(ctx, kPixelsPurge, redis.Z{Score: float64(purge.Unix()), Member: id})
	pipe.ZAdd(ctx, kOrgDeleted(p.OrgID), redis.Z{Score: float64(del.Unix()), Member: id})
	if _, err := pipe.Exec(ctx); err != nil {
		return p, err
	}
	return p, nil
}

// ExpungeDuePixels permanently removes every soft-deleted pixel whose
// retention window elapsed before `now`. Runs in batches so a large backlog
// never produces one giant command. Returns how many pixels were expunged.
func (r *RedisStore) ExpungeDuePixels(ctx context.Context, now time.Time) (int, error) {
	const batch = 100
	max := strconv.FormatInt(now.Unix(), 10)
	total := 0
	for {
		ids, err := r.c.ZRangeByScore(ctx, kPixelsPurge, &redis.ZRangeBy{
			Min: "-inf", Max: max, Offset: 0, Count: batch,
		}).Result()
		if err != nil {
			return total, err
		}
		if len(ids) == 0 {
			return total, nil
		}
		for _, id := range ids {
			if err := r.expungePixel(ctx, id, now); err != nil {
				return total, err
			}
			total++
		}
		if len(ids) < batch {
			return total, nil
		}
	}
}

// expungePixel removes one pixel's metadata, counters, and index entries.
// Bucket counter keys are enumerated deterministically (their labels are
// date-derived and TTL-bounded), so no SCAN is needed. UNLINK keeps the
// deletes off Redis's main thread.
func (r *RedisStore) expungePixel(ctx context.Context, id string, now time.Time) error {
	p, err := r.GetPixel(ctx, id)
	orgKnown := err == nil
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}

	keys := make([]string, 0, 2+35+73)
	keys = append(keys, kPixel+id, kPixelCount+id)
	for i := 0; i < 35; i++ {
		keys = append(keys, kPixelCountDay+id+":"+dayLabel(now.Add(time.Duration(-i)*24*time.Hour)))
	}
	for i := 0; i < 73; i++ {
		keys = append(keys, kPixelCountHour+id+":"+hourLabel(now.Add(time.Duration(-i)*time.Hour)))
	}

	pipe := r.c.TxPipeline()
	pipe.Unlink(ctx, keys...)
	pipe.SRem(ctx, kPixelsDeleted, id)
	pipe.ZRem(ctx, kPixelsPurge, id)
	if orgKnown {
		pipe.ZRem(ctx, kOrgDeleted(p.OrgID), id)
		// Defensive: also clear live indexes in case a soft delete was
		// interrupted partway through.
		pipe.SRem(ctx, kPixels, id)
		pipe.SRem(ctx, kPixelOrg+p.OrgID, id)
		pipe.ZRem(ctx, kOrgCreated(p.OrgID), id)
		pipe.ZRem(ctx, kOrgName(p.OrgID), nameMember(p.Name, id))
		for _, tag := range p.Tags {
			pipe.ZRem(ctx, kOrgTag(p.OrgID, tag), id)
		}
	}
	_, err = pipe.Exec(ctx)
	return err
}

// ReindexPixels backfills catalog indexes for pixels created before the
// index schema existed (or after a schema bump). Guarded by a version key
// so it's a single GET on every subsequent boot.
func (r *RedisStore) ReindexPixels(ctx context.Context) error {
	v, err := r.c.Get(ctx, kPixelIndexVer).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return err
	}
	if v == pixelIndexVersion {
		return nil
	}
	pixels, err := r.ListAllPixels(ctx)
	if err != nil {
		return err
	}
	pipe := r.c.Pipeline()
	for _, p := range pixels {
		if p.DeletedAt != nil {
			continue
		}
		score := float64(p.CreatedAt.Unix())
		pipe.ZAdd(ctx, kOrgCreated(p.OrgID), redis.Z{Score: score, Member: p.ID})
		pipe.ZAdd(ctx, kOrgName(p.OrgID), redis.Z{Score: 0, Member: nameMember(p.Name, p.ID)})
		for _, tag := range p.Tags {
			pipe.ZAdd(ctx, kOrgTag(p.OrgID, tag), redis.Z{Score: score, Member: p.ID})
		}
	}
	pipe.Set(ctx, kPixelIndexVer, pixelIndexVersion, 0)
	_, err = pipe.Exec(ctx)
	return err
}

// --- Counters (hot path) ---

// dayLabel formats t as "YYYYMMDD" in UTC.
func dayLabel(t time.Time) string { return t.UTC().Format("20060102") }

// hourLabel formats t as "YYYYMMDDHH" in UTC.
func hourLabel(t time.Time) string { return t.UTC().Format("2006010215") }

// incrPixelScript bumps lifetime + daily + hourly buckets atomically in one
// round-trip, unless the pixel has been soft-deleted (O(1) SET membership
// check) — deleted pixels must stop counting immediately. Lua keeps this at
// the same single round-trip cost as the previous pipeline.
var incrPixelScript = redis.NewScript(`
if redis.call('SISMEMBER', KEYS[1], ARGV[1]) == 1 then
  return 0
end
local life = redis.call('INCR', KEYS[2])
redis.call('INCR', KEYS[3])
redis.call('EXPIRE', KEYS[3], ARGV[2])
redis.call('INCR', KEYS[4])
redis.call('EXPIRE', KEYS[4], ARGV[3])
return life
`)

// IncrPixel bumps lifetime + daily + hourly buckets for pixelID in a single
// round-trip. Soft-deleted pixels are skipped (returns 0). Returns the new
// lifetime count.
func (r *RedisStore) IncrPixel(ctx context.Context, pixelID string, t time.Time) (int64, error) {
	dayKey := kPixelCountDay + pixelID + ":" + dayLabel(t)
	hourKey := kPixelCountHour + pixelID + ":" + hourLabel(t)
	return incrPixelScript.Run(ctx, r.c,
		[]string{kPixelsDeleted, kPixelCount + pixelID, dayKey, hourKey},
		pixelID, dailyTTL, hourlyTTL,
	).Int64()
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

// GetPixelsDaily fetches daily series for many pixels in one MGET
// (len(ids)×days keys) instead of one round-trip per pixel. Used by the
// dashboard so a page of pixels costs two Redis calls, not 2N.
func (r *RedisStore) GetPixelsDaily(ctx context.Context, pixelIDs []string, days int, now time.Time) (map[string][]BucketPoint, error) {
	return r.bulkSeries(ctx, pixelIDs, days, func(i int) (string, string) {
		t := now.Add(time.Duration(-(days - 1 - i)) * 24 * time.Hour)
		return dayLabel(t), kPixelCountDay
	})
}

// GetPixelsHourly is the hourly counterpart of GetPixelsDaily.
func (r *RedisStore) GetPixelsHourly(ctx context.Context, pixelIDs []string, hours int, now time.Time) (map[string][]BucketPoint, error) {
	return r.bulkSeries(ctx, pixelIDs, hours, func(i int) (string, string) {
		t := now.Add(time.Duration(-(hours - 1 - i)) * time.Hour)
		return hourLabel(t), kPixelCountHour
	})
}

// bulkSeries builds the cross-product of pixel ids × bucket labels, fetches
// it with a single MGET, and reshapes the flat reply per pixel.
func (r *RedisStore) bulkSeries(ctx context.Context, pixelIDs []string, n int, bucket func(i int) (label, prefix string)) (map[string][]BucketPoint, error) {
	out := make(map[string][]BucketPoint, len(pixelIDs))
	if len(pixelIDs) == 0 || n <= 0 {
		return out, nil
	}
	labels := make([]string, n)
	var prefix string
	for i := 0; i < n; i++ {
		labels[i], prefix = bucket(i)
	}
	keys := make([]string, 0, len(pixelIDs)*n)
	for _, id := range pixelIDs {
		for i := 0; i < n; i++ {
			keys = append(keys, prefix+id+":"+labels[i])
		}
	}
	vals, err := r.c.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	for pi, id := range pixelIDs {
		pts := make([]BucketPoint, n)
		for i := 0; i < n; i++ {
			pts[i] = BucketPoint{Label: labels[i], Count: parseMGetInt64(vals[pi*n+i])}
		}
		out[id] = pts
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
