package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/asilluron/pixelgo/internal/models"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// handleSignupGET renders step 1 of the wizard (email + password). If the URL
// carries ?invite=TOKEN we pre-resolve the invite to show the invitee which
// org + role they're accepting.
func (s *Server) handleSignupGET(c echo.Context) error {
	// Already logged in? Jump to wherever they should land.
	if u, err := s.resolveUser(c); err == nil {
		if u.OrgID == "" && !u.IsSuperAdmin {
			return c.Redirect(http.StatusFound, "/signup/org")
		}
		return c.Redirect(http.StatusFound, "/admin")
	}

	data := map[string]any{
		"Error":       c.QueryParam("error"),
		"InviteToken": "",
		"InviteOrg":   "",
		"InviteRole":  "",
		"InviteEmail": "",
	}
	if tok := strings.TrimSpace(c.QueryParam("invite")); tok != "" {
		inv, err := s.auth.GetInviteByToken(c.Request().Context(), tok)
		if err == nil && inv.Accepted == nil && time.Now().Before(inv.ExpiresAt) {
			org, _ := s.auth.GetOrg(c.Request().Context(), inv.OrgID)
			data["InviteToken"] = inv.Token
			data["InviteOrg"] = org.Name
			data["InviteRole"] = string(inv.Role)
			data["InviteEmail"] = inv.Email
		}
	}
	return c.Render(http.StatusOK, "signup.html", data)
}

// handleSignupPOST performs the Supabase signup, writes the profile row, and
// either consumes the invite (if provided) or sends the user to step 2.
func (s *Server) handleSignupPOST(c echo.Context) error {
	ctx := c.Request().Context()
	email := strings.TrimSpace(c.FormValue("email"))
	password := c.FormValue("password")
	inviteToken := strings.TrimSpace(c.FormValue("invite"))

	if email == "" || password == "" {
		return c.Redirect(http.StatusFound, "/signup?error=missing")
	}

	// Resolve the invite before creating the user so we can fail fast on
	// expired/unknown tokens rather than leaving an orphan Supabase account.
	var invite *models.Invite
	if inviteToken != "" {
		inv, err := s.auth.GetInviteByToken(ctx, inviteToken)
		if err != nil || inv.Accepted != nil || time.Now().After(inv.ExpiresAt) {
			return c.Redirect(http.StatusFound, "/signup?error=invite")
		}
		invite = &inv
	}

	sess, err := s.supa.Signup(ctx, email, password)
	if err != nil {
		return c.Redirect(http.StatusFound, "/signup?error=exists")
	}
	// With mailer_autoconfirm=true we expect a session on signup. Defend
	// against misconfiguration by falling back to an explicit login.
	if sess.AccessToken == "" {
		sess, err = s.supa.LoginPassword(ctx, email, password)
		if err != nil {
			return c.Redirect(http.StatusFound, "/login?error=confirm")
		}
	}
	// Create the pixelgo profile row so downstream code has a consistent
	// per-user record for flags like is_super_admin.
	if err := s.auth.UpsertProfile(ctx, sess.User.ID, false); err != nil {
		c.Logger().Errorf("signup: upsert profile for %s: %v", sess.User.ID, err)
		return err
	}

	if invite != nil {
		if err := s.auth.AddMember(ctx, sess.User.ID, invite.OrgID, invite.Role); err != nil {
			c.Logger().Errorf("signup: add member %s→%s: %v", sess.User.ID, invite.OrgID, err)
			return err
		}
		if err := s.auth.MarkInviteAccepted(ctx, invite.ID, time.Now().UTC()); err != nil {
			c.Logger().Errorf("signup: mark invite %s accepted: %v", invite.ID, err)
			return err
		}
		s.setSessionCookies(c, sess)
		return c.Redirect(http.StatusFound, "/admin")
	}

	s.setSessionCookies(c, sess)
	return c.Redirect(http.StatusFound, "/signup/org")
}

// handleSignupOrgGET renders step 2 (pick an org name). Gated by
// requireSession. Users who already belong to an org skip past this.
func (s *Server) handleSignupOrgGET(c echo.Context) error {
	u := currentUser(c)
	if u.OrgID != "" {
		return c.Redirect(http.StatusFound, "/admin")
	}
	return c.Render(http.StatusOK, "signup_org.html", map[string]any{
		"Email": u.Email,
		"Error": c.QueryParam("error"),
	})
}

// handleSignupOrgPOST creates the org, makes the current user its owner, and
// sends them to the admin dashboard.
func (s *Server) handleSignupOrgPOST(c echo.Context) error {
	ctx := c.Request().Context()
	u := currentUser(c)
	if u.OrgID != "" {
		return c.Redirect(http.StatusFound, "/admin")
	}
	name := strings.TrimSpace(c.FormValue("name"))
	if name == "" {
		return c.Redirect(http.StatusFound, "/signup/org?error=name")
	}

	org := models.Org{
		ID:        uuid.NewString(),
		Name:      name,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.auth.CreateOrg(ctx, org); err != nil {
		return err
	}
	if err := s.auth.AddMember(ctx, u.ID, org.ID, models.RoleOwner); err != nil {
		// Best-effort: if adding membership fails we've got an orphan org.
		// Surface the error so the operator notices.
		return err
	}
	return c.Redirect(http.StatusFound, "/admin")
}
