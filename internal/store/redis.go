package store

import (
	"context"

	"github.com/redis/go-redis/v9"
)

// Redis key conventions (pixel data only; auth data lives in Postgres):
//
//	pixel:{id}                 -> JSON(Pixel)
//	pixel:org:{orgID}          -> SET of pixel IDs for that org
//	pixels                     -> SET of all pixel IDs
//	pixel:count:{id}           -> INT64 counter (INCR on hit)
//	pixel:count:d:{id}:{YYYYMMDD}
//	pixel:count:h:{id}:{YYYYMMDDHH}
const (
	kPixel          = "pixel:"
	kPixelOrg       = "pixel:org:"
	kPixels         = "pixels"
	kPixelCount     = "pixel:count:"   // +{id}
	kPixelCountDay  = "pixel:count:d:" // +{id}:{YYYYMMDD}
	kPixelCountHour = "pixel:count:h:" // +{id}:{YYYYMMDDHH}
)

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
