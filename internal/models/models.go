// Package models defines the core domain entities for pixelgo.
package models

import "time"

// Role is a per-org membership role. Super-admin is a separate boolean on the
// user (IsSuperAdmin) rather than a Role value, because it's global rather
// than tied to a single org.
type Role string

const (
	RoleOwner  Role = "owner"
	RoleEditor Role = "editor"
	RoleViewer Role = "viewer"
)

func (r Role) Valid() bool {
	return r == RoleOwner || r == RoleEditor || r == RoleViewer
}

// CanEdit reports whether the role is allowed to create or modify pixels.
func (r Role) CanEdit() bool { return r == RoleOwner || r == RoleEditor }

// OrgBilling is the billing-address bundle on an Org. Every field is
// optional — pixelgo only needs an org's name and ID to function; billing
// details are collected lazily when a tenant graduates to a paid plan.
type OrgBilling struct {
	Email        string `json:"email,omitempty"`
	Name         string `json:"name,omitempty"`
	AddressLine1 string `json:"address_line1,omitempty"`
	AddressLine2 string `json:"address_line2,omitempty"`
	City         string `json:"city,omitempty"`
	Region       string `json:"region,omitempty"`
	PostalCode   string `json:"postal_code,omitempty"`
	Country      string `json:"country,omitempty"` // ISO-3166-1 alpha-2.
	TaxID        string `json:"tax_id,omitempty"`
}

// Org represents a tenant that owns one or more tracking pixels. Only
// `Name` is required for core behaviour; the display (Slug, LogoURL,
// Website, Description) and Billing bundles are optional tenant metadata
// used by the settings UI and invoicing.
type Org struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Slug        string     `json:"slug,omitempty"`
	LogoURL     string     `json:"logo_url,omitempty"`
	Website     string     `json:"website,omitempty"`
	Description string     `json:"description,omitempty"`
	Billing     OrgBilling `json:"billing"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// User is the authenticated identity as pixelgo sees it. Credentials are
// owned by Supabase Auth; this struct only carries what the UI needs to
// render the dashboard and gate actions.
type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	OrgID        string    `json:"org_id,omitempty"`
	Role         Role      `json:"role,omitempty"`
	IsSuperAdmin bool      `json:"is_super_admin"`
	CreatedAt    time.Time `json:"created_at"`
}

// Invite is a pending membership link. Owners create invites, the tool surfaces
// the corresponding /invite/:token URL, and the invitee redeems it during
// signup (or, if already signed in, via an accept handler).
type Invite struct {
	ID        string     `json:"id"`
	OrgID     string     `json:"org_id"`
	Email     string     `json:"email"`
	Role      Role       `json:"role"`
	Token     string     `json:"token"`
	CreatedBy string     `json:"created_by"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	Accepted  *time.Time `json:"accepted_at,omitempty"`
}

// Pixel is the tracked asset. The ID is what appears in the /p/{id} URL.
type Pixel struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// PixelStat couples a pixel with its counters for admin listings.
type PixelStat struct {
	Pixel    Pixel `json:"pixel"`
	Total    int64 `json:"total"`
	Today    int64 `json:"today"`
	LastHour int64 `json:"last_hour"`
}

// APIKeyType distinguishes personal keys (scoped to a user's current
// membership) from org keys (scoped to an org, independent of any user).
type APIKeyType string

const (
	APIKeyTypePersonal APIKeyType = "personal"
	APIKeyTypeOrg      APIKeyType = "org"
)

// APIKey is a bearer credential for the customer-facing JSON API. The
// plaintext token is only ever known at creation time — the DB stores a
// bcrypt hash plus a short non-secret prefix used to narrow lookups.
//
// Exactly one of UserID / OrgID is set depending on Type:
//   - personal: UserID set, OrgID empty; authorizes whatever org the user
//     belongs to at request time.
//   - org:      OrgID set, UserID empty; authorizes that org directly.
type APIKey struct {
	ID         string     `json:"id"`
	Type       APIKeyType `json:"type"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	UserID     string     `json:"user_id,omitempty"`
	OrgID      string     `json:"org_id,omitempty"`
	CreatedBy  string     `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}
