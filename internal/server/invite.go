package server

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/asilluron/pixelgo/internal/models"
	"github.com/asilluron/pixelgo/internal/store"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// inviteTTL controls how long an invite link is valid. The DB default is also
// 14 days (see the schema); we pin it in Go as well so the value is explicit
// and auditable at the application layer.
const inviteTTL = 14 * 24 * time.Hour

// handleCreateInvite lets an org owner mint an invite link. Only owners and
// super-admins are allowed; editors and viewers cannot invite.
func (s *Server) handleCreateInvite(c echo.Context) error {
	u := currentUser(c)
	if !(u.IsSuperAdmin || u.Role == models.RoleOwner) {
		return echo.NewHTTPError(http.StatusForbidden, "owner only")
	}
	email := strings.TrimSpace(c.FormValue("email"))
	role := models.Role(strings.TrimSpace(c.FormValue("role")))
	if email == "" || (role != models.RoleEditor && role != models.RoleViewer) {
		return echo.NewHTTPError(http.StatusBadRequest, "email and role (editor|viewer) required")
	}
	orgID := u.OrgID
	if u.IsSuperAdmin {
		orgID = strings.TrimSpace(c.FormValue("org_id"))
		if orgID == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "org_id required for super-admin")
		}
	}

	tok, err := newInviteToken()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	inv := models.Invite{
		ID:        uuid.NewString(),
		OrgID:     orgID,
		Email:     email,
		Role:      role,
		Token:     tok,
		CreatedBy: u.ID,
		CreatedAt: now,
		ExpiresAt: now.Add(inviteTTL),
	}
	if err := s.auth.CreateInvite(c.Request().Context(), inv); err != nil {
		return err
	}
	// Dashboard re-renders and shows the new link in the list.
	return c.Redirect(http.StatusFound, "/admin?invited=1")
}

// handleInviteLanding is the public entry point for an invite URL. If the
// token is valid and unused, we forward to the signup wizard with ?invite=.
// If the user is already logged in, we apply the membership immediately.
func (s *Server) handleInviteLanding(c echo.Context) error {
	tok := strings.TrimSpace(c.Param("token"))
	if tok == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "missing token")
	}
	inv, err := s.auth.GetInviteByToken(c.Request().Context(), tok)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return c.Render(http.StatusOK, "invite_invalid.html", nil)
		}
		return err
	}
	if inv.Accepted != nil || time.Now().After(inv.ExpiresAt) {
		return c.Render(http.StatusOK, "invite_invalid.html", nil)
	}

	// Logged-in users can accept immediately (they might not even be the
	// invited email, but we intentionally allow this — the token is the
	// authority).
	if u, err := s.resolveUser(c); err == nil {
		if u.OrgID != "" {
			return echo.NewHTTPError(http.StatusBadRequest, "already a member of an org")
		}
		if err := s.auth.AddMember(c.Request().Context(), u.ID, inv.OrgID, inv.Role); err != nil {
			return err
		}
		if err := s.auth.MarkInviteAccepted(c.Request().Context(), inv.ID, time.Now().UTC()); err != nil {
			return err
		}
		return c.Redirect(http.StatusFound, "/admin")
	}

	return c.Redirect(http.StatusFound, "/signup?invite="+tok)
}

// newInviteToken returns a URL-safe random invite token.
func newInviteToken() (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
