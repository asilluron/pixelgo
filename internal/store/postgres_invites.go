package store

import (
	"context"
	"errors"
	"time"

	"github.com/asilluron/pixelgo/internal/models"
	"github.com/jackc/pgx/v5"
)

// --- Invites ---

func (s *PostgresStore) CreateInvite(ctx context.Context, inv models.Invite) error {
	_, err := s.p.Exec(ctx,
		`insert into public.invites
		 (id, org_id, email, role, token, created_by, created_at, expires_at)
		 values ($1,$2,$3,$4,$5,$6,$7,$8)`,
		inv.ID, inv.OrgID, inv.Email, string(inv.Role), inv.Token,
		nullableUUID(inv.CreatedBy), inv.CreatedAt, inv.ExpiresAt)
	return err
}

func (s *PostgresStore) GetInviteByToken(ctx context.Context, token string) (models.Invite, error) {
	var inv models.Invite
	var createdBy *string
	var accepted *time.Time
	var role string
	err := s.p.QueryRow(ctx,
		`select id::text, org_id::text, email, role::text, token,
		        coalesce(created_by::text, ''), created_at, expires_at, accepted_at
		   from public.invites where token = $1`, token,
	).Scan(&inv.ID, &inv.OrgID, &inv.Email, &role, &inv.Token,
		&inv.CreatedBy, &inv.CreatedAt, &inv.ExpiresAt, &accepted)
	if errors.Is(err, pgx.ErrNoRows) {
		return inv, ErrNotFound
	}
	if err != nil {
		return inv, err
	}
	inv.Role = models.Role(role)
	inv.Accepted = accepted
	_ = createdBy
	return inv, nil
}

func (s *PostgresStore) ListInvitesByOrg(ctx context.Context, orgID string) ([]models.Invite, error) {
	rows, err := s.p.Query(ctx,
		`select id::text, org_id::text, email, role::text, token,
		        coalesce(created_by::text, ''), created_at, expires_at, accepted_at
		   from public.invites
		  where org_id = $1 and accepted_at is null and expires_at > now()
		  order by created_at desc`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Invite
	for rows.Next() {
		var inv models.Invite
		var role string
		var accepted *time.Time
		if err := rows.Scan(&inv.ID, &inv.OrgID, &inv.Email, &role, &inv.Token,
			&inv.CreatedBy, &inv.CreatedAt, &inv.ExpiresAt, &accepted); err != nil {
			return nil, err
		}
		inv.Role = models.Role(role)
		inv.Accepted = accepted
		out = append(out, inv)
	}
	return out, rows.Err()
}

func (s *PostgresStore) MarkInviteAccepted(ctx context.Context, id string, at time.Time) error {
	_, err := s.p.Exec(ctx,
		`update public.invites set accepted_at = $2 where id = $1`, id, at)
	return err
}

// nullableUUID returns nil when s is empty so pgx writes NULL rather than
// attempting to parse an empty string as a uuid.
func nullableUUID(s string) any {
	if s == "" {
		return nil
	}
	return s
}
