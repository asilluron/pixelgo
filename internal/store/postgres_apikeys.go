package store

import (
	"context"
	"time"

	"github.com/asilluron/pixelgo/internal/models"
)

// --- API keys ---

// CreateAPIKey inserts a new api_keys row. The caller is responsible for
// generating the plaintext token and computing `hash` (bcrypt of the full
// token). `k.Prefix` is the short non-secret lookup value stored in plain
// text so we can narrow candidates without scanning the whole table.
func (s *PostgresStore) CreateAPIKey(ctx context.Context, k models.APIKey, hash string) error {
	_, err := s.p.Exec(ctx,
		`insert into public.api_keys
		 (id, type, name, prefix, hash, user_id, org_id, created_by, created_at)
		 values ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		k.ID, string(k.Type), k.Name, k.Prefix, hash,
		nullableUUID(k.UserID), nullableUUID(k.OrgID),
		nullableUUID(k.CreatedBy), k.CreatedAt,
	)
	return err
}

// ListAPIKeyCandidates returns all active (non-revoked) keys whose prefix
// matches `prefix`. For a well-chosen prefix length (>=12 chars) this is
// expected to return at most 1-2 rows, but callers must still bcrypt-compare
// the full token against each candidate's Hash.
func (s *PostgresStore) ListAPIKeyCandidates(ctx context.Context, prefix string) ([]APIKeyRecord, error) {
	rows, err := s.p.Query(ctx,
		`select id::text, type::text, name, prefix, hash,
		        coalesce(user_id::text,''), coalesce(org_id::text,''),
		        coalesce(created_by::text,''), created_at, last_used_at, revoked_at
		   from public.api_keys
		  where prefix = $1 and revoked_at is null`, prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIKeyRecord
	for rows.Next() {
		var r APIKeyRecord
		var typ string
		var lastUsed, revoked *time.Time
		if err := rows.Scan(&r.Key.ID, &typ, &r.Key.Name, &r.Key.Prefix, &r.Hash,
			&r.Key.UserID, &r.Key.OrgID, &r.Key.CreatedBy,
			&r.Key.CreatedAt, &lastUsed, &revoked); err != nil {
			return nil, err
		}
		r.Key.Type = models.APIKeyType(typ)
		r.Key.LastUsedAt = lastUsed
		r.Key.RevokedAt = revoked
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListAPIKeysForUser returns personal keys owned by userID (newest first).
// Revoked keys are included so the owner can audit them; consumers filter.
func (s *PostgresStore) ListAPIKeysForUser(ctx context.Context, userID string) ([]models.APIKey, error) {
	return s.listKeys(ctx,
		`where user_id = $1 order by created_at desc`, userID)
}

// ListAPIKeysForOrg returns org keys for orgID (newest first).
func (s *PostgresStore) ListAPIKeysForOrg(ctx context.Context, orgID string) ([]models.APIKey, error) {
	return s.listKeys(ctx,
		`where org_id = $1 and type = 'org' order by created_at desc`, orgID)
}

// TouchAPIKey records a successful authentication. Best-effort — the middleware
// ignores failures so hot-path requests never fail because of telemetry.
func (s *PostgresStore) TouchAPIKey(ctx context.Context, id string, at time.Time) error {
	_, err := s.p.Exec(ctx,
		`update public.api_keys set last_used_at = $2 where id = $1`, id, at)
	return err
}

// RevokeAPIKey sets revoked_at. Callers should treat subsequent lookups as
// ErrNotFound since ListAPIKeyCandidates filters on revoked_at is null.
func (s *PostgresStore) RevokeAPIKey(ctx context.Context, id string, at time.Time) error {
	_, err := s.p.Exec(ctx,
		`update public.api_keys set revoked_at = coalesce(revoked_at, $2) where id = $1`, id, at)
	return err
}

// listKeys runs a common SELECT for the two list helpers above.
func (s *PostgresStore) listKeys(ctx context.Context, where string, args ...any) ([]models.APIKey, error) {
	q := `select id::text, type::text, name, prefix,
	             coalesce(user_id::text,''), coalesce(org_id::text,''),
	             coalesce(created_by::text,''), created_at, last_used_at, revoked_at
	        from public.api_keys ` + where
	rows, err := s.p.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.APIKey
	for rows.Next() {
		var k models.APIKey
		var typ string
		var lastUsed, revoked *time.Time
		if err := rows.Scan(&k.ID, &typ, &k.Name, &k.Prefix,
			&k.UserID, &k.OrgID, &k.CreatedBy,
			&k.CreatedAt, &lastUsed, &revoked); err != nil {
			return nil, err
		}
		k.Type = models.APIKeyType(typ)
		k.LastUsedAt = lastUsed
		k.RevokedAt = revoked
		out = append(out, k)
	}
	return out, rows.Err()
}
