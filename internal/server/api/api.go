// Package api implements pixelgo's customer-facing JSON API under /api/v1.
//
// Auth is bearer-token only: callers send `Authorization: Bearer pxg_…`
// which resolves via the AuthStore to an org (and, for personal keys, the
// underlying user). Cookies and Supabase JWTs are intentionally not
// accepted — those are the admin UI's concern.
//
// Responses are wrapped ({"data": …, "meta": …}) and errors use a stable
// machine-readable `code`. See api/openapi.yaml for the full contract.
package api

import (
	"github.com/asilluron/pixelgo/internal/store"
	"github.com/labstack/echo/v4"
)

// Deps bundles the stores the handlers need. Kept small and read-only so
// wiring from server.New stays a one-liner.
type Deps struct {
	Pixels store.PixelStore
	Auth   store.AuthStore
}

// Handler is the API's router root. Register mounts the /v1 routes onto
// the given Echo group.
type Handler struct {
	deps Deps
}

// New constructs a Handler. It does no I/O.
func New(d Deps) *Handler { return &Handler{deps: d} }

// Register wires the versioned routes onto g. The caller is expected to
// pass a group rooted at /api — this keeps v1 vs future v2 mounts obvious
// at the call site:
//
//	apiGroup := e.Group("/api")
//	api.New(deps).Register(apiGroup)
func (h *Handler) Register(g *echo.Group) {
	v1 := g.Group("/v1", h.requireAPIKey)

	v1.GET("/me", h.handleMe)
	v1.GET("/org", h.handleOrg)

	v1.GET("/pixels", h.handleListPixels)
	v1.POST("/pixels", h.handleCreatePixel)
	v1.GET("/pixels/:id", h.handleGetPixel)
	v1.DELETE("/pixels/:id", h.handleDeletePixel)
	v1.GET("/pixels/:id/stats", h.handlePixelStats)
	v1.GET("/pixels/:id/timeseries", h.handlePixelTimeseries)

	// Bulk endpoint. Colon is used to signal "action on collection" à la
	// Google AIP-136; the router treats it as a literal path segment.
	v1.GET("/pixels:batchStats", h.handlePixelsBatchStats)
}
