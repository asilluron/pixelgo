package server

import (
	"net/http"
	"sort"
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

// handleDashboard renders the admin index. Super admins see every org;
// org members see their own org's pixels. Owners also see outstanding
// invites + a create-invite form.
func (s *Server) handleDashboard(c echo.Context) error {
	ctx := c.Request().Context()
	u := currentUser(c)

	var orgs []models.Org
	var pixels []models.Pixel
	var invites []models.Invite
	var err error

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
		pixels, err = s.pixels.ListAllPixels(ctx)
	} else {
		var org models.Org
		org, err = s.auth.GetOrg(ctx, u.OrgID)
		if err == nil {
			orgs = []models.Org{org}
			pixels, err = s.pixels.ListPixelsByOrg(ctx, u.OrgID)
		}
		if err == nil && u.Role == models.RoleOwner {
			invites, err = s.auth.ListInvitesByOrg(ctx, u.OrgID)
		}
	}
	if err != nil {
		return err
	}

	ids := make([]string, len(pixels))
	for i, p := range pixels {
		ids[i] = p.ID
	}
	now := time.Now().UTC()
	bundles, err := s.pixels.GetPixelBundles(ctx, ids, now)
	if err != nil {
		return err
	}

	stats := make([]models.PixelStat, len(pixels))
	for i, p := range pixels {
		b := bundles[p.ID]
		stats[i] = models.PixelStat{
			Pixel:    p,
			Total:    b.Total,
			Today:    b.Today,
			LastHour: b.LastHour,
		}
	}
	sort.Slice(stats, func(i, j int) bool { return stats[i].Total > stats[j].Total })

	// Per-pixel time-series for the dashboard charts. Last 7 daily buckets
	// + last 24 hourly buckets, keyed by pixel ID. html/template will emit
	// this as a JSON literal inside the inline <script> block on the page.
	chartData := make(map[string]pixelChart, len(pixels))
	for _, p := range pixels {
		daily, err := s.pixels.GetPixelDaily(ctx, p.ID, 7, now)
		if err != nil {
			return err
		}
		hourly, err := s.pixels.GetPixelHourly(ctx, p.ID, 24, now)
		if err != nil {
			return err
		}
		chartData[p.ID] = pixelChart{
			Daily:  toChartSeries(daily),
			Hourly: toChartSeries(hourly),
		}
	}

	orgNames := make(map[string]string, len(orgs))
	for _, o := range orgs {
		orgNames[o.ID] = o.Name
	}

	var totalViews, todayViews, hourViews int64
	for _, s := range stats {
		totalViews += s.Total
		todayViews += s.Today
		hourViews += s.LastHour
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

// handleCreatePixel creates a new pixel. Owners and editors (and super-admins)
// may create pixels. Super-admins must specify an org_id; everyone else uses
// their own org.
func (s *Server) handleCreatePixel(c echo.Context) error {
	u := currentUser(c)
	if !u.IsSuperAdmin && !u.Role.CanEdit() {
		return echo.NewHTTPError(http.StatusForbidden, "insufficient role")
	}
	name := strings.TrimSpace(c.FormValue("name"))
	orgID := strings.TrimSpace(c.FormValue("org_id"))
	if !u.IsSuperAdmin {
		orgID = u.OrgID
	}
	if name == "" || orgID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "name and org_id required")
	}
	if _, err := s.auth.GetOrg(c.Request().Context(), orgID); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "org not found")
	}

	p := models.Pixel{
		ID:        uuid.NewString(),
		OrgID:     orgID,
		Name:      name,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.pixels.CreatePixel(c.Request().Context(), p); err != nil {
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
