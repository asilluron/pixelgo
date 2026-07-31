package store

import (
	"context"

	"github.com/redis/go-redis/v9"
)

// Redis key conventions (pixel data only; auth data lives in Postgres):
//
//	pixel:{id}                 -> JSON(Pixel)
//	pixel:org:{orgID}          -> SET of live pixel IDs for that org
//	pixels                     -> SET of all live pixel IDs
//	pixel:count:{id}           -> INT64 counter (INCR on hit)
//	pixel:count:d:{id}:{YYYYMMDD}
//	pixel:count:h:{id}:{YYYYMMDDHH}
//
// Catalog secondary indexes (live pixels only). These make list/sort/filter
// O(log N + page) server-side instead of O(N) full-set loads, which matters
// for orgs with thousands of pixels:
//
//	pixel:org:{orgID}:z:created     -> ZSET  member=id, score=created unix
//	pixel:org:{orgID}:z:name        -> ZSET  member="{lower(name)}\x00{id}", score=0 (lex)
//	pixel:org:{orgID}:z:tag:{tag}   -> ZSET  member=id, score=created unix
//
// Soft-delete bookkeeping:
//
//	pixel:org:{orgID}:z:deleted     -> ZSET  member=id, score=deleted unix
//	pixels:deleted                  -> SET   of soft-deleted ids (hot-path guard)
//	pixels:purge                    -> ZSET  member=id, score=purge-at unix
//	pixel:index:v                   -> STRING index schema version (reindex marker)
const (
	kPixel          = "pixel:"
	kPixelOrg       = "pixel:org:"
	kPixels         = "pixels"
	kPixelCount     = "pixel:count:"   // +{id}
	kPixelCountDay  = "pixel:count:d:" // +{id}:{YYYYMMDD}
	kPixelCountHour = "pixel:count:h:" // +{id}:{YYYYMMDDHH}

	kPixelsDeleted = "pixels:deleted"
	kPixelsPurge   = "pixels:purge"
	kPixelIndexVer = "pixel:index:v"
)

// pixelIndexVersion is bumped whenever the catalog index schema changes;
// ReindexPixels rebuilds the indexes when the stored version doesn't match.
const pixelIndexVersion = "1"

// Per-org catalog index keys.
func kOrgCreated(orgID string) string  { return kPixelOrg + orgID + ":z:created" }
func kOrgName(orgID string) string     { return kPixelOrg + orgID + ":z:name" }
func kOrgTag(orgID, tag string) string { return kPixelOrg + orgID + ":z:tag:" + tag }
func kOrgDeleted(orgID string) string  { return kPixelOrg + orgID + ":z:deleted" }

// Bucket TTLs — retain enough history for trend views without unbounded growth.
const (
	dailyTTL  = 35 * 24 * 3600 // 35 days in seconds
	hourlyTTL = 72 * 3600      // 72 hours in seconds
)

// RedisStore is a PixelStore backed by a single Redis instance.
type RedisStore struct {
	c *redis.Client
}

// NewRedis parses a redis:// URL and returns a connected RedisStore.
func NewRedis(ctx context.Context, url string) (*RedisStore, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, err
	}
	c := redis.NewClient(opts)
	if err := c.Ping(ctx).Err(); err != nil {
		_ = c.Close()
		return nil, err
	}
	return &RedisStore{c: c}, nil
}

func (r *RedisStore) Ping(ctx context.Context) error { return r.c.Ping(ctx).Err() }
func (r *RedisStore) Close() error                   { return r.c.Close() }
