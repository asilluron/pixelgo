package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/asilluron/pixelgo/internal/models"
	"github.com/asilluron/pixelgo/internal/server/api"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// handleCreateAPIKey mints either a personal key (scoped to the caller) or
// an org key (scoped to the caller's org). Org keys require the `owner` role
// — editors and viewers can only create personal keys. The plaintext token
// is shown once via a flash-style query string on the dashboard redirect;
// it is never stored server-side in plaintext.
func (s *Server) handleCreateAPIKey(c echo.Context) error {
	u := currentUser(c)
	name := strings.TrimSpace(c.FormValue("name"))
	kind := strings.TrimSpace(c.FormValue("type"))
	if name == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "name required")
	}
	if kind != string(models.APIKeyTypePersonal) && kind != string(models.APIKeyTypeOrg) {
		return echo.NewHTTPError(http.StatusBadRequest, "type must be personal or org")
	}
	keyType := models.APIKeyType(kind)

	if keyType == models.APIKeyTypeOrg && !(u.IsSuperAdmin || u.Role == models.RoleOwner) {
		return echo.NewHTTPError(http.StatusForbidden, "owner only")
	}
	if u.OrgID == "" && !u.IsSuperAdmin {
		return echo.NewHTTPError(http.StatusBadRequest, "no active org")
	}

	var apiKind api.Kind
	k := models.APIKey{
		ID:        uuid.NewString(),
		Type:      keyType,
		Name:      name,
		CreatedBy: u.ID,
		CreatedAt: time.Now().UTC(),
	}
	switch keyType {
	case models.APIKeyTypePersonal:
		k.UserID = u.ID
		apiKind = api.KindPersonal
	case models.APIKeyTypeOrg:
		k.OrgID = u.OrgID
		apiKind = api.KindOrg
	}

	token, prefix, hash, err := api.NewToken(apiKind)
	if err != nil {
		return err
	}
	k.Prefix = prefix

	if err := s.auth.CreateAPIKey(c.Request().Context(), k, hash); err != nil {
		return err
	}
	// The plaintext token is forwarded via query string once so the dashboard
	// can reveal it. This is acceptable because /admin is same-origin, over
	// TLS in production, and the URL lives only as long as the redirect.
	return c.Redirect(http.StatusFound, "/admin?new_key="+token)
}

// handleRevokeAPIKey flips revoked_at on the given key. Callers may only
// revoke keys they own (or any org-key for their org if they're an owner /
// super-admin); enforced by re-listing keys first and verifying membership.
func (s *Server) handleRevokeAPIKey(c echo.Context) error {
	u := currentUser(c)
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id required")
	}
	ctx := c.Request().Context()

	personal, err := s.auth.ListAPIKeysForUser(ctx, u.ID)
	if err != nil {
		return err
	}
	orgKeys, err := s.auth.ListAPIKeysForOrg(ctx, u.OrgID)
	if err != nil {
		return err
	}
	owns := func(list []models.APIKey) bool {
		for _, k := range list {
			if k.ID == id {
				return true
			}
		}
		return false
	}
	isOwnerRole := u.IsSuperAdmin || u.Role == models.RoleOwner
	if !owns(personal) && !(isOwnerRole && owns(orgKeys)) {
		return echo.NewHTTPError(http.StatusForbidden, "not your key")
	}
	if err := s.auth.RevokeAPIKey(ctx, id, time.Now().UTC()); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin")
}
