package api

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/asilluron/pixelgo/internal/models"
	"github.com/asilluron/pixelgo/internal/store"
	"github.com/labstack/echo/v4"
)

// Echo context keys for auth data resolved by requireAPIKey.
const (
	ctxAPIKey = "api_key"
	ctxOrgID  = "api_org_id"
	ctxUserID = "api_user_id"
)

// requireAPIKey authenticates the bearer token and attaches the resolved
// APIKey plus its effective org/user to the request context. All /api/v1
// routes run behind this middleware.
func (h *Handler) requireAPIKey(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		raw, err := bearerToken(c)
		if err != nil {
			return unauthorized(c, "missing Authorization header")
		}
		kind, prefix, err := ParseToken(raw)
		if err != nil {
			return unauthorized(c, "malformed token")
		}

		ctx := c.Request().Context()
		cands, err := h.deps.Auth.ListAPIKeyCandidates(ctx, prefix)
		if err != nil {
			return internal(c, err)
		}

		var matched *models.APIKey
		for i := range cands {
			if CompareHash(cands[i].Hash, raw) == nil {
				matched = &cands[i].Key
				break
			}
		}
		if matched == nil {
			return unauthorized(c, "invalid token")
		}
		// Defense in depth: kind tag in the token must agree with the DB row.
		if !kindMatches(kind, matched.Type) {
			return unauthorized(c, "invalid token")
		}

		orgID, userID, err := h.resolveScope(c, matched)
		if err != nil {
			return err
		}
		c.Set(ctxAPIKey, *matched)
		c.Set(ctxOrgID, orgID)
		c.Set(ctxUserID, userID)

		// Telemetry is best-effort — never block the request on an audit write.
		// Detach from the request context so client disconnect doesn't drop it.
		go func(id string) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = h.deps.Auth.TouchAPIKey(ctx, id, time.Now().UTC())
		}(matched.ID)

		return next(c)
	}
}

// resolveScope returns the effective (orgID, userID) for a key. Personal keys
// derive org from the owner's current membership, so a revoked membership
// silently de-authorizes the key. Org keys are self-scoped.
func (h *Handler) resolveScope(c echo.Context, k *models.APIKey) (string, string, error) {
	switch k.Type {
	case models.APIKeyTypeOrg:
		return k.OrgID, "", nil
	case models.APIKeyTypePersonal:
		orgID, _, err := h.deps.Auth.GetMembershipForUser(c.Request().Context(), k.UserID)
		if errors.Is(err, store.ErrNotFound) || orgID == "" {
			return "", "", forbidden(c, "key owner has no active org membership")
		}
		if err != nil {
			return "", "", internal(c, err)
		}
		return orgID, k.UserID, nil
	default:
		return "", "", unauthorized(c, "invalid token")
	}
}

// bearerToken extracts the value after "Bearer " (case-insensitive prefix).
func bearerToken(c echo.Context) (string, error) {
	h := c.Request().Header.Get(echo.HeaderAuthorization)
	if h == "" {
		return "", errors.New("missing")
	}
	const p = "bearer "
	if len(h) <= len(p) || !strings.EqualFold(h[:len(p)], p) {
		return "", errors.New("malformed")
	}
	return strings.TrimSpace(h[len(p):]), nil
}

func kindMatches(k Kind, t models.APIKeyType) bool {
	switch t {
	case models.APIKeyTypePersonal:
		return k == KindPersonal
	case models.APIKeyTypeOrg:
		return k == KindOrg
	}
	return false
}

// Accessors for handlers.

func apiKey(c echo.Context) models.APIKey {
	k, _ := c.Get(ctxAPIKey).(models.APIKey)
	return k
}

func orgID(c echo.Context) string {
	s, _ := c.Get(ctxOrgID).(string)
	return s
}

func userID(c echo.Context) string {
	s, _ := c.Get(ctxUserID).(string)
	return s
}
