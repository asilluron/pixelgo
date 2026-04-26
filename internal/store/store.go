// Package store defines the persistence interfaces used by pixelgo and ships
// Redis (pixel metadata + counters) and Postgres/Supabase (orgs, members,
// invites, super-admin profiles) implementations.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/asilluron/pixelgo/internal/models"
)

// ErrNotFound is returned when a lookup finds no matching record.
var ErrNotFound = errors.New("store: not found")

// ErrAlreadyExists is returned when a create would violate uniqueness.
var ErrAlreadyExists = errors.New("store: already exists")

// CountBundle is a compact view of a pixel's counters used by the dashboard.
type CountBundle struct {
	Total    int64 `json:"total"`
	Today    int64 `json:"today"`
	LastHour int64 `json:"last_hour"`
}

// BucketPoint is a single point in a daily or hourly time series.
type BucketPoint struct {
	Label string `json:"label"`
	Count int64  `json:"count"`
}

// PixelStore is the hot-path store: pixel metadata and counters. Implemented
// by RedisStore.
type PixelStore interface {
	CreatePixel(ctx context.Context, p models.Pixel) error
	GetPixel(ctx context.Context, id string) (models.Pixel, error)
	ListPixelsByOrg(ctx context.Context, orgID string) ([]models.Pixel, error)
	ListAllPixels(ctx context.Context) ([]models.Pixel, error)

	IncrPixel(ctx context.Context, pixelID string, t time.Time) (int64, error)
	GetPixelCount(ctx context.Context, pixelID string) (int64, error)
	GetPixelCounts(ctx context.Context, pixelIDs []string) (map[string]int64, error)
	GetPixelBundles(ctx context.Context, pixelIDs []string, now time.Time) (map[string]CountBundle, error)
	GetPixelDaily(ctx context.Context, pixelID string, days int, now time.Time) ([]BucketPoint, error)
	GetPixelHourly(ctx context.Context, pixelID string, hours int, now time.Time) ([]BucketPoint, error)

	Ping(ctx context.Context) error
	Close() error
}

// AuthStore is the identity-and-tenancy store: orgs, memberships, invites, and
// the super-admin flag on profiles. Implemented by PostgresStore on top of
// Supabase. Supabase Auth owns passwords and sessions; this interface only
// tracks the data that lives in our own tables.
type AuthStore interface {
	// Orgs. CreateOrg only stores the baseline (id/name/timestamps);
	// UpdateOrgProfile overwrites the optional display + billing fields
	// with whatever the caller supplies, mapping empty strings to NULL.
	CreateOrg(ctx context.Context, o models.Org) error
	GetOrg(ctx context.Context, id string) (models.Org, error)
	ListOrgs(ctx context.Context) ([]models.Org, error)
	UpdateOrgProfile(ctx context.Context, o models.Org) error

	// Memberships
	AddMember(ctx context.Context, userID, orgID string, role models.Role) error
	GetMembershipForUser(ctx context.Context, userID string) (orgID string, role models.Role, err error)

	// Profiles (super-admin flag)
	UpsertProfile(ctx context.Context, userID string, isSuperAdmin bool) error
	IsSuperAdmin(ctx context.Context, userID string) (bool, error)
	CountSuperAdmins(ctx context.Context) (int64, error)

	// Invites
	CreateInvite(ctx context.Context, inv models.Invite) error
	GetInviteByToken(ctx context.Context, token string) (models.Invite, error)
	ListInvitesByOrg(ctx context.Context, orgID string) ([]models.Invite, error)
	MarkInviteAccepted(ctx context.Context, id string, at time.Time) error

	// API keys. Lookups are two-step: callers first fetch active candidates
	// for a short prefix and then bcrypt-compare the full token against each
	// stored hash. The plaintext token is never persisted.
	CreateAPIKey(ctx context.Context, k models.APIKey, hash string) error
	ListAPIKeyCandidates(ctx context.Context, prefix string) ([]APIKeyRecord, error)
	ListAPIKeysForUser(ctx context.Context, userID string) ([]models.APIKey, error)
	ListAPIKeysForOrg(ctx context.Context, orgID string) ([]models.APIKey, error)
	TouchAPIKey(ctx context.Context, id string, at time.Time) error
	RevokeAPIKey(ctx context.Context, id string, at time.Time) error

	Ping(ctx context.Context) error
	Close() error
}

// APIKeyRecord is a candidate returned by ListAPIKeyCandidates. It pairs the
// APIKey metadata with its bcrypt hash so callers can compare without a
// follow-up query.
type APIKeyRecord struct {
	Key  models.APIKey
	Hash string
}
