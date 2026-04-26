package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/asilluron/pixelgo/internal/models"
	"github.com/asilluron/pixelgo/internal/store"
	"github.com/asilluron/pixelgo/internal/supaauth"
	"github.com/labstack/echo/v4"
)

const (
	accessCookieName  = "pixelgo_access"
	refreshCookieName = "pixelgo_refresh"
	ctxUserKey        = "pixelgo_user"
	ctxTokenKey       = "pixelgo_access_token"
)

// handleLoginGET renders the sign-in form. Already-logged-in users are
// bounced to /admin.
func (s *Server) handleLoginGET(c echo.Context) error {
	if _, err := s.resolveUser(c); err == nil {
		return c.Redirect(http.StatusFound, "/admin")
	}
	return c.Render(http.StatusOK, "login.html", map[string]any{
		"Error": c.QueryParam("error"),
	})
}

// handleLoginPOST calls Supabase password grant and issues JWT cookies.
func (s *Server) handleLoginPOST(c echo.Context) error {
	email := strings.TrimSpace(c.FormValue("email"))
	password := c.FormValue("password")
	sess, err := s.supa.LoginPassword(c.Request().Context(), email, password)
	if err != nil {
		return c.Redirect(http.StatusFound, "/login?error=invalid")
	}
	s.setSessionCookies(c, sess)
	return c.Redirect(http.StatusFound, "/admin")
}

// handleLogout clears cookies and best-effort revokes the token.
func (s *Server) handleLogout(c echo.Context) error {
	if ck, err := c.Cookie(accessCookieName); err == nil && ck.Value != "" {
		_ = s.supa.Logout(c.Request().Context(), ck.Value)
	}
	s.clearSessionCookies(c)
	return c.Redirect(http.StatusFound, "/login")
}

// setSessionCookies writes access + refresh tokens for the browser. The
// cookie Max-Age is the configured session TTL; the JWT inside expires
// sooner (Supabase default 1h), at which point the middleware refreshes.
func (s *Server) setSessionCookies(c echo.Context, sess *supaauth.Session) {
	secure := s.cfg.Env == "production"
	maxAge := int(s.cfg.SessionTTL.Seconds())
	c.SetCookie(&http.Cookie{
		Name: accessCookieName, Value: sess.AccessToken,
		Path: "/", HttpOnly: true, Secure: secure,
		SameSite: http.SameSiteLaxMode, MaxAge: maxAge,
	})
	c.SetCookie(&http.Cookie{
		Name: refreshCookieName, Value: sess.RefreshToken,
		Path: "/", HttpOnly: true, Secure: secure,
		SameSite: http.SameSiteLaxMode, MaxAge: maxAge,
	})
}

func (s *Server) clearSessionCookies(c echo.Context) {
	for _, name := range []string{accessCookieName, refreshCookieName} {
		c.SetCookie(&http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1})
	}
}

// requireSession loads the current user (or refreshes the session via the
// refresh cookie) and stores it in the Echo context. Unauthenticated
// requests are redirected to /login.
func (s *Server) requireSession(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		u, err := s.resolveUser(c)
		if err != nil {
			return c.Redirect(http.StatusFound, "/login")
		}
		c.Set(ctxUserKey, u)
		return next(c)
	}
}

// requireOrg gates handlers that need an active org membership. A logged-in
// user that has not yet finished the signup wizard (no org) is sent to
// /signup/org to pick a name. Super-admins bypass this check.
func (s *Server) requireOrg(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := currentUser(c)
		if u.IsSuperAdmin {
			return next(c)
		}
		if u.OrgID == "" {
			return c.Redirect(http.StatusFound, "/signup/org")
		}
		return next(c)
	}
}

// resolveUser parses the access cookie, verifies the JWT, and enriches the
// user with membership/profile data from Postgres. If the access token is
// expired or invalid it tries the refresh cookie once, and rewrites both
// cookies on success so the browser gets a fresh access token.
func (s *Server) resolveUser(c echo.Context) (models.User, error) {
	ctx := c.Request().Context()

	access, _ := c.Cookie(accessCookieName)
	if access != nil && access.Value != "" {
		if claims, err := s.jwts.Verify(access.Value); err == nil {
			u, err := s.hydrateUser(ctx, claims.Subject, claims.Email)
			if err != nil {
				return models.User{}, err
			}
			c.Set(ctxTokenKey, access.Value)
			return u, nil
		}
	}

	// Access missing/expired — try refresh.
	refresh, _ := c.Cookie(refreshCookieName)
	if refresh == nil || refresh.Value == "" {
		return models.User{}, errors.New("no session")
	}
	sess, err := s.supa.RefreshSession(ctx, refresh.Value)
	if err != nil {
		s.clearSessionCookies(c)
		return models.User{}, err
	}
	s.setSessionCookies(c, sess)
	claims, err := s.jwts.Verify(sess.AccessToken)
	if err != nil {
		return models.User{}, err
	}
	u, err := s.hydrateUser(ctx, claims.Subject, claims.Email)
	if err != nil {
		return models.User{}, err
	}
	c.Set(ctxTokenKey, sess.AccessToken)
	return u, nil
}

// hydrateUser loads per-user data (super-admin flag + org membership) from
// Postgres. A user that exists in Supabase but has neither a profile row
// nor a membership row (e.g. just after signup) is returned with empty
// OrgID/Role; callers decide how to route them.
func (s *Server) hydrateUser(ctx context.Context, userID, email string) (models.User, error) {
	u := models.User{ID: userID, Email: email}
	isSuper, err := s.auth.IsSuperAdmin(ctx, userID)
	if err != nil {
		return u, err
	}
	u.IsSuperAdmin = isSuper

	orgID, role, err := s.auth.GetMembershipForUser(ctx, userID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return u, err
	}
	u.OrgID = orgID
	u.Role = role
	return u, nil
}

func currentUser(c echo.Context) models.User {
	u, _ := c.Get(ctxUserKey).(models.User)
	return u
}
