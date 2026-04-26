package api

import (
	"errors"

	"github.com/asilluron/pixelgo/internal/store"
	"github.com/labstack/echo/v4"
)

// handleOrg returns the caller's org record. Personal and org keys both
// resolve to exactly one org via requireAPIKey, so no path parameter is
// needed and multi-tenancy is implicit.
func (h *Handler) handleOrg(c echo.Context) error {
	org, err := h.deps.Auth.GetOrg(c.Request().Context(), orgID(c))
	if errors.Is(err, store.ErrNotFound) {
		return notFound(c, "org not found")
	}
	if err != nil {
		return internal(c, err)
	}
	return ok(c, org)
}
