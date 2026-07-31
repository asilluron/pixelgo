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

// PixelSort enumerates the server-side orderings supported by ListPixels.
type PixelSort string

const (
	SortCreatedDesc PixelSort = "created_desc"
	SortCreatedAsc  PixelSort = "created_asc"
	SortNameAsc     PixelSort = "name_asc"
	SortNameDesc    PixelSort = "name_desc"
)

// ValidPixelSort reports whether s is a recognized sort order.
func ValidPixelSort(s PixelSort) bool {
	switch s {
	case SortCreatedDesc, SortCreatedAsc, SortNameAsc, SortNameDesc:
		return true
	}
	return false
}

// PixelStatus selects live vs soft-deleted pixels in ListPixels.
type PixelStatus string

const (
	StatusActive  PixelStatus = "active"
	StatusDeleted PixelStatus = "deleted"
)

// PixelListOptions drives the catalog query. Constraints (enforced by the
// store so every caller gets index-backed queries):
//   - Tag and NamePrefix are mutually exclusive.
//   - NamePrefix implies name ordering (created sorts are coerced).
//   - Tag implies created ordering (name sorts are coerced).
//   - Status=deleted ignores Tag/NamePrefix and orders by deletion time.
type PixelListOptions struct {
	OrgID      string
	Status     PixelStatus // default StatusActive
	Tag        string      // filter: exact tag match
	NamePrefix string      // filter: case-insensitive name prefix
	Sort       PixelSort   // default SortCreatedDesc
	Limit      int         // page size; must be > 0
	Offset     int
}

// PixelPage is one page of catalog results plus the total match count.
type PixelPage struct {
	Pixels []models.Pixel
	Total  int64
}

// PixelStore is the hot-path store: pixel metadata and counters. Implemented
// by RedisStore.
type PixelStore interface {
	CreatePixel(ctx context.Context, p models.Pixel) error
	GetPixel(ctx context.Context, id string) (models.Pixel, error)
	GetPixels(ctx context.Context, ids []string) ([]models.Pixel, error)
	ListPixelsByOrg(ctx context.Context, orgID string) ([]models.Pixel, error)
	ListPixelIDsByOrg(ctx context.Context, orgID string) ([]string, error)
	ListAllPixels(ctx context.Context) ([]models.Pixel, error)

	// Catalog: index-backed sort/filter/pagination.
	ListPixels(ctx context.Context, opts PixelListOptions) (PixelPage, error)

	// Soft delete + retention. SoftDeletePixel is idempotent: deleting an
	// already-deleted pixel returns its current state. ExpungeDuePixels
	// permanently removes pixels whose retention window has elapsed.
	SoftDeletePixel(ctx context.Context, id string, at time.Time) (models.Pixel, error)
	ExpungeDuePixels(ctx context.Context, now time.Time) (int, error)

	// ReindexPixels rebuilds catalog indexes for pixels created before the
	// index schema existed. No-op once the stored version matches.
	ReindexPixels(ctx context.Context) error

	IncrPixel(ctx context.Context, pixelID string, t time.Time) (int64, error)
	GetPixelCount(ctx context.Context, pixelID string) (int64, error)
	GetPixelCounts(ctx context.Context, pixelIDs []string) (map[string]int64, error)
	GetPixelBundles(ctx context.Context, pixelIDs []string, now time.Time) (map[string]CountBundle, error)
	GetPixelDaily(ctx context.Context, pixelID string, days int, now time.Time) ([]BucketPoint, error)
	GetPixelHourly(ctx context.Context, pixelID string, hours int, now time.Time) ([]BucketPoint, error)
	GetPixelsDaily(ctx context.Context, pixelIDs []string, days int, now time.Time) (map[string][]BucketPoint, error)
	GetPixelsHourly(ctx context.Context, pixelIDs []string, hours int, now time.Time) (map[string][]BucketPoint, error)

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
