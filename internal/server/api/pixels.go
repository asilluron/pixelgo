package api

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/asilluron/pixelgo/internal/models"
	"github.com/asilluron/pixelgo/internal/store"
	"github.com/labstack/echo/v4"
)

// listMeta is returned alongside a list of pixels. `count` is the total
// number of pixels in the current response; v1 does not paginate because a
// single org's pixel set is small by design.
type listMeta struct {
	Count int `json:"count"`
}

// handleListPixels returns every pixel owned by the caller's org.
func (h *Handler) handleListPixels(c echo.Context) error {
	pixels, err := h.deps.Pixels.ListPixelsByOrg(c.Request().Context(), orgID(c))
	if err != nil {
		return internal(c, err)
	}
	sort.Slice(pixels, func(i, j int) bool { return pixels[i].CreatedAt.After(pixels[j].CreatedAt) })
	return ok(c, pixels, listMeta{Count: len(pixels)})
}

// handleGetPixel returns a single pixel scoped to the caller's org.
func (h *Handler) handleGetPixel(c echo.Context) error {
	p, err := h.fetchOwnedPixel(c)
	if err != nil {
		return err
	}
	return ok(c, p)
}

// statsResponse wraps CountBundle with the pixel id for unambiguous client
// handling, especially in batch responses.
type statsResponse struct {
	PixelID  string `json:"pixel_id"`
	Total    int64  `json:"total"`
	Today    int64  `json:"today"`
	LastHour int64  `json:"last_hour"`
}

func (h *Handler) handlePixelStats(c echo.Context) error {
	p, err := h.fetchOwnedPixel(c)
	if err != nil {
		return err
	}
	bundles, err := h.deps.Pixels.GetPixelBundles(c.Request().Context(), []string{p.ID}, time.Now().UTC())
	if err != nil {
		return internal(c, err)
	}
	b := bundles[p.ID]
	return ok(c, statsResponse{PixelID: p.ID, Total: b.Total, Today: b.Today, LastHour: b.LastHour})
}

// handlePixelsBatchStats serves GET /pixels:batchStats?ids=a,b,c. All ids
// must belong to the caller's org — any foreign id yields 403 rather than
// leaking existence.
func (h *Handler) handlePixelsBatchStats(c echo.Context) error {
	raw := strings.TrimSpace(c.QueryParam("ids"))
	if raw == "" {
		return badRequest(c, "query parameter `ids` is required")
	}
	ids := splitAndTrim(raw, ',')
	if len(ids) == 0 || len(ids) > 200 {
		return badRequest(c, "1–200 ids required")
	}

	ctx := c.Request().Context()
	owned, err := h.deps.Pixels.ListPixelsByOrg(ctx, orgID(c))
	if err != nil {
		return internal(c, err)
	}
	ownedSet := make(map[string]struct{}, len(owned))
	for _, p := range owned {
		ownedSet[p.ID] = struct{}{}
	}
	for _, id := range ids {
		if _, ok := ownedSet[id]; !ok {
			return forbidden(c, "id not accessible: "+id)
		}
	}

	bundles, err := h.deps.Pixels.GetPixelBundles(ctx, ids, time.Now().UTC())
	if err != nil {
		return internal(c, err)
	}
	out := make([]statsResponse, 0, len(ids))
	for _, id := range ids {
		b := bundles[id]
		out = append(out, statsResponse{PixelID: id, Total: b.Total, Today: b.Today, LastHour: b.LastHour})
	}
	return ok(c, out, listMeta{Count: len(out)})
}

// handlePixelTimeseries serves both hourly and daily granularities via the
// `granularity` query parameter. `window` caps how many buckets are returned
// and is clamped to the storage TTL (72 hours / 35 days).
func (h *Handler) handlePixelTimeseries(c echo.Context) error {
	p, err := h.fetchOwnedPixel(c)
	if err != nil {
		return err
	}
	gran := strings.ToLower(c.QueryParam("granularity"))
	if gran == "" {
		gran = "day"
	}
	window, err := strconv.Atoi(strings.TrimSpace(c.QueryParam("window")))
	if err != nil || window <= 0 {
		window = 24
		if gran == "day" {
			window = 7
		}
	}

	ctx := c.Request().Context()
	now := time.Now().UTC()
	var points []store.BucketPoint
	switch gran {
	case "hour":
		if window > 72 {
			window = 72
		}
		points, err = h.deps.Pixels.GetPixelHourly(ctx, p.ID, window, now)
	case "day":
		if window > 35 {
			window = 35
		}
		points, err = h.deps.Pixels.GetPixelDaily(ctx, p.ID, window, now)
	default:
		return badRequest(c, "granularity must be `hour` or `day`")
	}
	if err != nil {
		return internal(c, err)
	}
	return ok(c, points, map[string]any{
		"pixel_id":    p.ID,
		"granularity": gran,
		"window":      window,
		"count":       len(points),
	})
}

// fetchOwnedPixel looks up :id and enforces org scoping — a pixel owned by
// another org is reported as 404 to avoid leaking existence.
func (h *Handler) fetchOwnedPixel(c echo.Context) (models.Pixel, error) {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return models.Pixel{}, badRequest(c, "pixel id required")
	}
	p, err := h.deps.Pixels.GetPixel(c.Request().Context(), id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && p.OrgID != orgID(c)) {
		return models.Pixel{}, notFound(c, "pixel not found")
	}
	if err != nil {
		return models.Pixel{}, internal(c, err)
	}
	return p, nil
}

// splitAndTrim splits s on sep, trims whitespace around each field, and
// discards empties.
func splitAndTrim(s string, sep rune) []string {
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == sep })
	out := parts[:0]
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
