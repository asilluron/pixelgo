package server

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/asilluron/pixelgo/internal/models"
	"github.com/asilluron/pixelgo/internal/store"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// chartSeries is the labels/counts pair consumed by the dashboard's
// Chart.js snippet. Defined here (not in models) because it's a view-model
// that only exists to shape data for the admin template.
type chartSeries struct {
	Labels []string `json:"labels"`
	Counts []int64  `json:"counts"`
}

// pixelChart bundles the last-7-day line chart with the last-24-hour bar
// chart for a single pixel. The admin handler builds a map[pixelID]pixelChart
// and passes it through to dashboard.html.
type pixelChart struct {
	Daily  chartSeries `json:"daily"`
	Hourly chartSeries `json:"hourly"`
}

// toChartSeries flattens a []store.BucketPoint into parallel label/count
// arrays so Chart.js can consume them without a second pass in JS.
func toChartSeries(pts []store.BucketPoint) chartSeries {
	cs := chartSeries{
		Labels: make([]string, len(pts)),
		Counts: make([]int64, len(pts)),
	}
	for i, p := range pts {
		cs.Labels[i] = p.Label
		cs.Counts[i] = p.Count
	}
	return cs
}

// dashboardPageSize is how many pixels render per dashboard page. Charts and
// cards are only built for the visible page, so orgs with thousands of
// pixels get constant-cost page loads.
const dashboardPageSize = 20

// catalogView carries the dashboard's search/sort/pagination state into the
// template. Nil means catalog controls are hidden (super-admin global view).
type catalogView struct {
	Query      string
	Tag        string
	Sort       string
	Page       int
	TotalPages int
	Total      int64
	PrevURL    string
	NextURL    string
	Filtered   bool
}

// catalogURL rebuilds /admin?… preserving the current filters with a new page.
func catalogURL(q, tag, sortSel string, page int) string {
	v := url.Values{}
	if q != "" {
		v.Set("q", q)
	}
	if tag != "" {
		v.Set("tag", tag)
	}
	if sortSel != "" && sortSel != "views" {
		v.Set("sort", sortSel)
	}
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}
	if len(v) == 0 {
		return "/admin"
	}
	return "/admin?" + v.Encode()
}

// handleDashboard renders the admin index. Super admins see every org;
// org members see their own org's pixels with catalog controls (search,
// tag filter, sort, pagination). Owners also see outstanding invites +
// a create-invite form.
func (s *Server) handleDashboard(c echo.Context) error {
	ctx := c.Request().Context()
	u := currentUser(c)
	now := time.Now().UTC()

	var (
		orgs       []models.Org
		invites    []models.Invite
		pagePixels []models.Pixel
		summaryIDs []string // every live pixel id feeding the totals cards
		catalog    *catalogView
		err        error
	)

	if u.IsSuperAdmin {
		orgs, err = s.auth.ListOrgs(ctx)
		if err != nil {
			return err
		}
		// Fresh install: a super-admin with no orgs in the system has nothing
		// useful to do on the dashboard (the invite + create-pixel forms both
		// need a target org). Force them through the create-org step first.
		if len(orgs) == 0 {
			return c.Redirect(http.StatusFound, "/signup/org")
		}
		pagePixels, err = s.pixels.ListAllPixels(ctx)
		if err != nil {
			return err
		}
		summaryIDs = pixelIDs(pagePixels)
	} else {
		var org models.Org
		org, err = s.auth.GetOrg(ctx, u.OrgID)
		if err != nil {
			return err
		}
		orgs = []models.Org{org}
		if u.Role == models.RoleOwner {
			invites, err = s.auth.ListInvitesByOrg(ctx, u.OrgID)
			if err != nil {
				return err
			}
		}
		summaryIDs, err = s.pixels.ListPixelIDsByOrg(ctx, u.OrgID)
		if err != nil {
			return err
		}
	}

	// One MGET for every live pixel's counters: feeds the summary cards and
	// (for the default sort) the by-views ordering.
	bundles, err := s.pixels.GetPixelBundles(ctx, summaryIDs, now)
	if err != nil {
		return err
	}

	if u.IsSuperAdmin {
		sort.Slice(pagePixels, func(i, j int) bool {
			return bundles[pagePixels[i].ID].Total > bundles[pagePixels[j].ID].Total
		})
	} else {
		pagePixels, catalog, err = s.catalogPage(c, u.OrgID, summaryIDs, bundles)
		if err != nil {
			return err
		}
	}

	stats := make([]models.PixelStat, len(pagePixels))
	for i, p := range pagePixels {
		b := bundles[p.ID]
		stats[i] = models.PixelStat{
			Pixel:    p,
			Total:    b.Total,
			Today:    b.Today,
			LastHour: b.LastHour,
		}
	}

	// Time-series for the visible page only: two bulk MGETs total instead of
	// two round-trips per pixel.
	pageIDs := pixelIDs(pagePixels)
	daily, err := s.pixels.GetPixelsDaily(ctx, pageIDs, 7, now)
	if err != nil {
		return err
	}
	hourly, err := s.pixels.GetPixelsHourly(ctx, pageIDs, 24, now)
	if err != nil {
		return err
	}
	chartData := make(map[string]pixelChart, len(pageIDs))
	for _, id := range pageIDs {
		chartData[id] = pixelChart{
			Daily:  toChartSeries(daily[id]),
			Hourly: toChartSeries(hourly[id]),
		}
	}

	orgNames := make(map[string]string, len(orgs))
	for _, o := range orgs {
		orgNames[o.ID] = o.Name
	}

	var totalViews, todayViews, hourViews int64
	for _, id := range summaryIDs {
		b := bundles[id]
		totalViews += b.Total
		todayViews += b.Today
		hourViews += b.LastHour
	}

	scheme := schemeOf(c)
	host := c.Request().Host
	inviteLinks := make([]map[string]string, 0, len(invites))
	for _, inv := range invites {
		inviteLinks = append(inviteLinks, map[string]string{
			"Email": inv.Email,
			"Role":  string(inv.Role),
			"URL":   scheme + "://" + host + "/invite/" + inv.Token,
		})
	}

	// API keys: the caller's personal keys, plus the org's shared keys if
	// they're an owner (only owners can mint/revoke org keys).
	personalKeys, err := s.auth.ListAPIKeysForUser(ctx, u.ID)
	if err != nil {
		return err
	}
	var orgAPIKeys []models.APIKey
	if u.OrgID != "" && (u.IsSuperAdmin || u.Role == models.RoleOwner) {
		orgAPIKeys, err = s.auth.ListAPIKeysForOrg(ctx, u.OrgID)
		if err != nil {
			return err
		}
	}

	return c.Render(http.StatusOK, "dashboard.html", map[string]any{
		"User":         u,
		"IsSuper":      u.IsSuperAdmin,
		"CanEdit":      u.IsSuperAdmin || u.Role.CanEdit(),
		"IsOwner":      u.Role == models.RoleOwner,
		"Orgs":         orgs,
		"OrgNames":     orgNames,
		"Stats":        stats,
		"Catalog":      catalog,
		"TotalViews":   totalViews,
		"TodayViews":   todayViews,
		"HourViews":    hourViews,
		"InviteLinks":  inviteLinks,
		"JustInvited":  c.QueryParam("invited"),
		"Host":         host,
		"Scheme":       scheme,
		"PersonalKeys": personalKeys,
		"OrgAPIKeys":   orgAPIKeys,
		"NewKey":       c.QueryParam("new_key"),
		"ChartData":    chartData,
	})
}

// catalogPage resolves the dashboard's search/sort/page query params into one
// page of pixels. The default "views" sort reuses the already-fetched bundles
// (no extra Redis work); every other sort/filter runs against the store's
// secondary indexes.
func (s *Server) catalogPage(c echo.Context, orgID string, allIDs []string, bundles map[string]store.CountBundle) ([]models.Pixel, *catalogView, error) {
	ctx := c.Request().Context()
	q := strings.TrimSpace(c.QueryParam("q"))
	tag := strings.ToLower(strings.TrimSpace(c.QueryParam("tag")))
	if q != "" {
		tag = "" // search wins if both are present
	}
	sortSel := c.QueryParam("sort")
	switch sortSel {
	case "views", "newest", "oldest", "name_asc", "name_desc":
	default:
		sortSel = "views"
	}
	page, _ := strconv.Atoi(c.QueryParam("page"))
	if page < 1 {
		page = 1
	}

	var (
		pixels []models.Pixel
		total  int64
		err    error
	)
	if sortSel == "views" && q == "" && tag == "" {
		// By-views ordering needs counters, which we already hold for every
		// live pixel — sort ids in memory and fetch only the page's metadata.
		ids := make([]string, len(allIDs))
		copy(ids, allIDs)
		sort.Slice(ids, func(i, j int) bool {
			bi, bj := bundles[ids[i]].Total, bundles[ids[j]].Total
			if bi != bj {
				return bi > bj
			}
			return ids[i] < ids[j]
		})
		total = int64(len(ids))
		lo := (page - 1) * dashboardPageSize
		if lo > len(ids) {
			lo = len(ids)
		}
		hi := lo + dashboardPageSize
		if hi > len(ids) {
			hi = len(ids)
		}
		pixels, err = s.pixels.GetPixels(ctx, ids[lo:hi])
	} else {
		opts := store.PixelListOptions{
			OrgID:      orgID,
			NamePrefix: q,
			Tag:        tag,
			Limit:      dashboardPageSize,
			Offset:     (page - 1) * dashboardPageSize,
		}
		switch sortSel {
		case "oldest":
			opts.Sort = store.SortCreatedAsc
		case "name_asc":
			opts.Sort = store.SortNameAsc
		case "name_desc":
			opts.Sort = store.SortNameDesc
		default: // "newest", or "views" coerced under a filter
			opts.Sort = store.SortCreatedDesc
		}
		var res store.PixelPage
		res, err = s.pixels.ListPixels(ctx, opts)
		pixels, total = res.Pixels, res.Total
	}
	if err != nil {
		return nil, nil, err
	}

	totalPages := int((total + dashboardPageSize - 1) / dashboardPageSize)
	if totalPages < 1 {
		totalPages = 1
	}
	cv := &catalogView{
		Query:      q,
		Tag:        tag,
		Sort:       sortSel,
		Page:       page,
		TotalPages: totalPages,
		Total:      total,
		Filtered:   q != "" || tag != "",
	}
	if page > 1 {
		cv.PrevURL = catalogURL(q, tag, sortSel, page-1)
	}
	if page < totalPages {
		cv.NextURL = catalogURL(q, tag, sortSel, page+1)
	}
	return pixels, cv, nil
}

// pixelIDs projects a pixel slice onto its ids.
func pixelIDs(pixels []models.Pixel) []string {
	out := make([]string, len(pixels))
	for i, p := range pixels {
		out[i] = p.ID
	}
	return out
}

// handleCreatePixel creates a new pixel from the dashboard's quick-create
// form. Owners and editors (and super-admins) may create pixels; the URL and
// tags fields are optional catalog metadata. Super-admins must specify an
// org_id; everyone else uses their own org.
func (s *Server) handleCreatePixel(c echo.Context) error {
	u := currentUser(c)
	if !u.IsSuperAdmin && !u.Role.CanEdit() {
		return echo.NewHTTPError(http.StatusForbidden, "insufficient role")
	}
	orgID := strings.TrimSpace(c.FormValue("org_id"))
	if !u.IsSuperAdmin {
		orgID = u.OrgID
	}
	if orgID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "org_id required")
	}
	name, err := models.ValidatePixelName(c.FormValue("name"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	pageURL, err := models.ValidatePixelURL(c.FormValue("url"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	tags, err := models.NormalizeTags(strings.Split(c.FormValue("tags"), ","))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if _, err := s.auth.GetOrg(c.Request().Context(), orgID); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "org not found")
	}

	p := models.Pixel{
		ID:        uuid.NewString(),
		OrgID:     orgID,
		Name:      name,
		URL:       pageURL,
		Tags:      tags,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.pixels.CreatePixel(c.Request().Context(), p); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin")
}

// handleDeletePixel soft-deletes a pixel from the dashboard. The pixel stops
// counting immediately and is expunged 30 days later by the retention worker.
func (s *Server) handleDeletePixel(c echo.Context) error {
	u := currentUser(c)
	if !u.IsSuperAdmin && !u.Role.CanEdit() {
		return echo.NewHTTPError(http.StatusForbidden, "insufficient role")
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "pixel id required")
	}
	ctx := c.Request().Context()
	p, err := s.pixels.GetPixel(ctx, id)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "pixel not found")
	}
	if !u.IsSuperAdmin && p.OrgID != u.OrgID {
		return echo.NewHTTPError(http.StatusNotFound, "pixel not found")
	}
	if _, err := s.pixels.SoftDeletePixel(ctx, id, time.Now().UTC()); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin")
}

func schemeOf(c echo.Context) string {
	if c.Request().TLS != nil {
		return "https"
	}
	if xf := c.Request().Header.Get("X-Forwarded-Proto"); xf != "" {
		return xf
	}
	return "http"
}
