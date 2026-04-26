package store

import (
	"context"
	"errors"
	"time"

	"github.com/asilluron/pixelgo/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStore implements AuthStore against a Supabase-managed Postgres
// instance. It connects through the transaction pooler (port 6543) and uses
// simple SQL — no RLS is required because the service role / pooler user
// bypasses it anyway.
type PostgresStore struct {
	p *pgxpool.Pool
}

// NewPostgres dials the Supabase transaction pooler and verifies the
// connection. Caller is responsible for Close.
func NewPostgres(ctx context.Context, dsn string) (*PostgresStore, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	// Keep pool small; we're a single-purpose service.
	cfg.MaxConns = 8
	cfg.MinConns = 1
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.MaxConnLifetime = 30 * time.Minute
	// Supabase's transaction pooler (port 6543) multiplexes client connections
	// over a smaller set of backend sessions, so named prepared statements
	// created by one client can collide (SQLSTATE 42P05) with another's. Use
	// Exec mode so pgx sends unnamed statements each round-trip. Fine for
	// our tiny query volume; avoids the collision entirely.
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &PostgresStore{p: pool}, nil
}

func (s *PostgresStore) Ping(ctx context.Context) error { return s.p.Ping(ctx) }
func (s *PostgresStore) Close() error                   { s.p.Close(); return nil }

// --- Orgs ---

// orgSelectCols is the common projection used by every org read. Nullable
// columns are coalesced to the empty string so they scan straight into
// plain Go strings without the caller juggling sql.NullString.
const orgSelectCols = `
    id::text, name,
    coalesce(slug,''), coalesce(logo_url,''),
    coalesce(website,''), coalesce(description,''),
    coalesce(billing_email,''), coalesce(billing_name,''),
    coalesce(billing_address_line1,''), coalesce(billing_address_line2,''),
    coalesce(billing_city,''), coalesce(billing_region,''),
    coalesce(billing_postal_code,''), coalesce(billing_country,''),
    coalesce(tax_id,''),
    created_at, updated_at`

// scanOrg reads a single row with the orgSelectCols layout into o.
func scanOrg(row pgx.Row, o *models.Org) error {
	return row.Scan(
		&o.ID, &o.Name,
		&o.Slug, &o.LogoURL, &o.Website, &o.Description,
		&o.Billing.Email, &o.Billing.Name,
		&o.Billing.AddressLine1, &o.Billing.AddressLine2,
		&o.Billing.City, &o.Billing.Region,
		&o.Billing.PostalCode, &o.Billing.Country,
		&o.Billing.TaxID,
		&o.CreatedAt, &o.UpdatedAt,
	)
}

func (s *PostgresStore) CreateOrg(ctx context.Context, o models.Org) error {
	_, err := s.p.Exec(ctx,
		`insert into public.orgs (id, name, created_at, updated_at)
		 values ($1,$2,$3,$3)`,
		o.ID, o.Name, o.CreatedAt)
	return err
}

func (s *PostgresStore) GetOrg(ctx context.Context, id string) (models.Org, error) {
	var o models.Org
	err := scanOrg(
		s.p.QueryRow(ctx, `select `+orgSelectCols+` from public.orgs where id = $1`, id),
		&o,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return o, ErrNotFound
	}
	return o, err
}

func (s *PostgresStore) ListOrgs(ctx context.Context) ([]models.Org, error) {
	rows, err := s.p.Query(ctx,
		`select `+orgSelectCols+` from public.orgs order by name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Org
	for rows.Next() {
		var o models.Org
		if err := scanOrg(rows, &o); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// UpdateOrgProfile overwrites the display + billing fields on an org. The
// caller passes a fully-populated models.Org (including Name); empty string
// values map to SQL NULL so the DB stays clean rather than full of blanks.
func (s *PostgresStore) UpdateOrgProfile(ctx context.Context, o models.Org) error {
	_, err := s.p.Exec(ctx,
		`update public.orgs set
		     name = $2,
		     slug = $3, logo_url = $4, website = $5, description = $6,
		     billing_email = $7, billing_name = $8,
		     billing_address_line1 = $9, billing_address_line2 = $10,
		     billing_city = $11, billing_region = $12,
		     billing_postal_code = $13, billing_country = $14,
		     tax_id = $15,
		     updated_at = now()
		   where id = $1`,
		o.ID, o.Name,
		nullableText(o.Slug), nullableText(o.LogoURL),
		nullableText(o.Website), nullableText(o.Description),
		nullableText(o.Billing.Email), nullableText(o.Billing.Name),
		nullableText(o.Billing.AddressLine1), nullableText(o.Billing.AddressLine2),
		nullableText(o.Billing.City), nullableText(o.Billing.Region),
		nullableText(o.Billing.PostalCode), nullableText(o.Billing.Country),
		nullableText(o.Billing.TaxID),
	)
	return err
}

// nullableText returns nil for empty strings so pgx writes SQL NULL
// instead of a blank value. Mirrors nullableUUID in postgres_invites.go.
func nullableText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// --- Memberships ---

func (s *PostgresStore) AddMember(ctx context.Context, userID, orgID string, role models.Role) error {
	_, err := s.p.Exec(ctx,
		`insert into public.org_members (user_id, org_id, role) values ($1,$2,$3)`,
		userID, orgID, string(role))
	return err
}

func (s *PostgresStore) GetMembershipForUser(ctx context.Context, userID string) (string, models.Role, error) {
	var orgID, role string
	err := s.p.QueryRow(ctx,
		`select org_id::text, role::text from public.org_members where user_id = $1 limit 1`,
		userID,
	).Scan(&orgID, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", err
	}
	return orgID, models.Role(role), nil
}

// --- Profiles (super_admin flag) ---

func (s *PostgresStore) UpsertProfile(ctx context.Context, userID string, isSuperAdmin bool) error {
	_, err := s.p.Exec(ctx,
		`insert into public.profiles (user_id, is_super_admin) values ($1,$2)
		 on conflict (user_id) do update set is_super_admin = excluded.is_super_admin`,
		userID, isSuperAdmin)
	return err
}

func (s *PostgresStore) IsSuperAdmin(ctx context.Context, userID string) (bool, error) {
	var v bool
	err := s.p.QueryRow(ctx,
		`select is_super_admin from public.profiles where user_id = $1`, userID,
	).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return v, err
}

func (s *PostgresStore) CountSuperAdmins(ctx context.Context) (int64, error) {
	var n int64
	err := s.p.QueryRow(ctx,
		`select count(*) from public.profiles where is_super_admin = true`,
	).Scan(&n)
	return n, err
}
