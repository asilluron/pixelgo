package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/asilluron/pixelgo/internal/models"
	"github.com/asilluron/pixelgo/internal/store"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// listMeta is returned alongside a list of pixels. `count` is the number of
// items in the current page; `total` is the number of matches across all
// pages for the applied filters.
type listMeta struct {
	Count int `json:"count"`
}

// pageMeta extends listMeta with pagination fields for the catalog listing.
type pageMeta struct {
	Count  int    `json:"count"`
	Total  int64  `json:"total"`
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`
	Sort   string `json:"sort"`
}

const (
	defaultPageSize = 50
	maxPageSize     = 200
)

// handleListPixels serves the pixel catalog: index-backed filtering
// (`q` name prefix, `tag`, `status`), sorting, and limit/offset pagination —
// built for orgs with thousands of pixels, so nothing here loads the full
// set. Ordering rules when filters apply: `q` implies name order, `tag`
// implies created order, `status=deleted` orders by deletion time.
func (h *Handler) handleListPixels(c echo.Context) error {
	opts := store.PixelListOptions{
		OrgID:      orgID(c),
		NamePrefix: strings.TrimSpace(c.QueryParam("q")),
		Tag:        strings.ToLower(strings.TrimSpace(c.QueryParam("tag"))),
		Sort:       store.SortCreatedDesc,
		Limit:      defaultPageSize,
	}
	if opts.NamePrefix != "" && opts.Tag != "" {
		return badRequest(c, "`q` and `tag` cannot be combined")
	}
	if s := c.QueryParam("sort"); s != "" {
		opts.Sort = store.PixelSort(s)
		if !store.ValidPixelSort(opts.Sort) {
			return badRequest(c, "sort must be one of created_desc, created_asc, name_asc, name_desc")
		}
	}
	switch status := c.QueryParam("status"); status {
	case "", "active":
		opts.Status = store.StatusActive
	case "deleted":
		opts.Status = store.StatusDeleted
	default:
		return badRequest(c, "status must be `active` or `deleted`")
	}
	if raw := c.QueryParam("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxPageSize {
			return badRequest(c, "limit must be an integer between 1 and 200")
		}
		opts.Limit = n
	}
	if raw := c.QueryParam("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return badRequest(c, "offset must be a non-negative integer")
		}
		opts.Offset = n
	}

	page, err := h.deps.Pixels.ListPixels(c.Request().Context(), opts)
	if err != nil {
		return internal(c, err)
	}
	return ok(c, page.Pixels, pageMeta{
		Count:  len(page.Pixels),
		Total:  page.Total,
		Limit:  opts.Limit,
		Offset: opts.Offset,
		Sort:   string(opts.Sort),
	})
}

// createPixelRequest is the POST /pixels body. `url` and `tags` are optional
// catalog metadata — a quick create is just `{"name": "Product page"}`.
type createPixelRequest struct {
	Name string   `json:"name"`
	URL  string   `json:"url"`
	Tags []string `json:"tags"`
}

// handleCreatePixel lets API callers mint pixels dynamically (e.g. one per
// product page at publish time). Requires an org key or a personal key whose
// owner can edit (owner/editor).
func (h *Handler) handleCreatePixel(c echo.Context) error {
	if !role(c).CanEdit() {
		return forbidden(c, "this key cannot create pixels (viewer role)")
	}
	var req createPixelRequest
	if err := c.Bind(&req); err != nil {
		return badRequest(c, "invalid JSON body")
	}
	name, err := models.ValidatePixelName(req.Name)
	if err != nil {
		return badRequest(c, err.Error())
	}
	pageURL, err := models.ValidatePixelURL(req.URL)
	if err != nil {
		return badRequest(c, err.Error())
	}
	tags, err := models.NormalizeTags(req.Tags)
	if err != nil {
		return badRequest(c, err.Error())
	}

	p := models.Pixel{
		ID:        uuid.NewString(),
		OrgID:     orgID(c),
		Name:      name,
		URL:       pageURL,
		Tags:      tags,
		CreatedAt: time.Now().UTC(),
	}
	if err := h.deps.Pixels.CreatePixel(c.Request().Context(), p); err != nil {
		return internal(c, err)
	}
	return c.JSON(http.StatusCreated, Success{Data: p})
}

// deletePixelResponse confirms a soft delete and tells the caller when the
// data will be permanently expunged.
type deletePixelResponse struct {
	ID        string     `json:"id"`
	DeletedAt *time.Time `json:"deleted_at"`
	PurgeAt   *time.Time `json:"purge_at"`
}

// handleDeletePixel soft-deletes a pixel: it disappears from live listings
// and stops counting immediately, and its data is expunged 30 days later.
// Idempotent — deleting an already-deleted pixel returns its current state.
func (h *Handler) handleDeletePixel(c echo.Context) error {
	if !role(c).CanEdit() {
		return forbidden(c, "this key cannot delete pixels (viewer role)")
	}
	p, err := h.fetchOwnedPixel(c)
	if err != nil {
		return err
	}
	p, err = h.deps.Pixels.SoftDeletePixel(c.Request().Context(), p.ID, time.Now().UTC())
	if errors.Is(err, store.ErrNotFound) {
		return notFound(c, "pixel not found")
	}
	if err != nil {
		return internal(c, err)
	}
	return ok(c, deletePixelResponse{ID: p.ID, DeletedAt: p.DeletedAt, PurgeAt: p.PurgeAt()})
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
