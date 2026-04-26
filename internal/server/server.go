// Package server wires the HTTP router, middleware, and handlers.
package server

import (
	"context"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"time"

	"github.com/asilluron/pixelgo/internal/config"
	"github.com/asilluron/pixelgo/internal/server/api"
	"github.com/asilluron/pixelgo/internal/store"
	"github.com/asilluron/pixelgo/internal/supaauth"
	"github.com/asilluron/pixelgo/web"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"golang.org/x/time/rate"
)

// Server bundles everything needed to serve HTTP traffic.
type Server struct {
	cfg    *config.Config
	pixels store.PixelStore
	auth   store.AuthStore
	supa   *supaauth.Client
	jwts   *supaauth.Verifier
	echo   *echo.Echo
	tmpls  *template.Template
}

// New builds a fully-wired *Server.
func New(
	cfg *config.Config,
	pixels store.PixelStore,
	auth store.AuthStore,
	supa *supaauth.Client,
	jwts *supaauth.Verifier,
	tmplFS fs.FS,
) (*Server, error) {
	funcs := template.FuncMap{"commas": humanCommas}
	tmpls, err := template.New("").Funcs(funcs).ParseFS(tmplFS, "*.html")
	if err != nil {
		return nil, err
	}

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	if cfg.TemplatesDir != "" {
		log.Printf("templates: live-reload from %s (dev)", cfg.TemplatesDir)
		e.Renderer = &devRenderer{fsys: tmplFS, funcs: funcs}
	} else {
		e.Renderer = &renderer{t: tmpls}
	}

	srv := &Server{
		cfg: cfg, pixels: pixels, auth: auth,
		supa: supa, jwts: jwts,
		echo: e, tmpls: tmpls,
	}

	e.Use(middleware.Recover())
	e.Use(middleware.RequestIDWithConfig(middleware.RequestIDConfig{}))
	e.Use(middleware.LoggerWithConfig(middleware.LoggerConfig{
		Skipper: func(c echo.Context) bool {
			return len(c.Path()) >= 2 && c.Path()[:2] == "/p"
		},
		Format: `{"ts":"${time_rfc3339}","id":"${id}","method":"${method}","uri":"${uri}","status":${status},"latency_ms":${latency}}` + "\n",
	}))

	srv.routes()
	return srv, nil
}

// Start blocks serving HTTP on the configured address.
func (s *Server) Start() error { return s.echo.Start(s.cfg.Addr) }

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error { return s.echo.Shutdown(ctx) }

// Handler returns the underlying http.Handler. Intended for tests.
func (s *Server) Handler() http.Handler { return s.echo }

func (s *Server) routes() {
	// Hot path: tracking pixel.
	s.echo.GET("/p/:id", s.handlePixel, s.pixelRateLimiter())

	// Health.
	s.echo.GET("/healthz", func(c echo.Context) error {
		if err := s.pixels.Ping(c.Request().Context()); err != nil {
			return c.String(http.StatusServiceUnavailable, "degraded")
		}
		if err := s.auth.Ping(c.Request().Context()); err != nil {
			return c.String(http.StatusServiceUnavailable, "degraded")
		}
		return c.String(http.StatusOK, "ok")
	})

	// Root: marketing landing page. Logged-in users still flow through the
	// CTA into /signup or /login, which redirect to /admin as appropriate.
	s.echo.GET("/", func(c echo.Context) error {
		return c.Render(http.StatusOK, "index.html", nil)
	})

	// Public static assets (OpenGraph image, etc.). Served from the embedded
	// web/static FS — no live-reload, intentionally; these are immutable per
	// release. Browsers and OG crawlers may cache aggressively.
	s.echo.GET("/static/*", echo.WrapHandler(http.StripPrefix("/static/",
		http.FileServer(http.FS(web.Static())))))

	// Auth.
	s.echo.GET("/login", s.handleLoginGET)
	s.echo.POST("/login", s.handleLoginPOST)
	s.echo.POST("/logout", s.handleLogout)

	// Signup wizard.
	s.echo.GET("/signup", s.handleSignupGET)
	s.echo.POST("/signup", s.handleSignupPOST)
	s.echo.GET("/signup/org", s.handleSignupOrgGET, s.requireSession)
	s.echo.POST("/signup/org", s.handleSignupOrgPOST, s.requireSession)

	// Invite landing (public): redirects to /signup?invite=token.
	s.echo.GET("/invite/:token", s.handleInviteLanding)

	// Admin UI (session + org membership required).
	admin := s.echo.Group("/admin", s.requireSession, s.requireOrg)
	admin.GET("", s.handleDashboard)
	admin.GET("/", s.handleDashboard)
	admin.POST("/pixels", s.handleCreatePixel)
	admin.POST("/invites", s.handleCreateInvite)
	admin.POST("/api-keys", s.handleCreateAPIKey)
	admin.POST("/api-keys/:id/revoke", s.handleRevokeAPIKey)

	// Customer-facing JSON API. Self-contained package; see internal/server/api.
	apiGroup := s.echo.Group("/api")
	api.New(api.Deps{Pixels: s.pixels, Auth: s.auth}).Register(apiGroup)

	// Public docs. /openapi.yaml feeds tooling, /docs renders Swagger UI.
	s.echo.GET("/openapi.yaml", s.handleOpenAPISpec)
	s.echo.GET("/docs", s.handleDocs)
}

// pixelRateLimiter returns an Echo middleware that token-bucket rate-limits
// requests by c.RealIP(). When PIXELGO_RL_DISABLE=1 it returns a pass-through.
func (s *Server) pixelRateLimiter() echo.MiddlewareFunc {
	if s.cfg.RateLimitDisabled {
		return func(next echo.HandlerFunc) echo.HandlerFunc { return next }
	}
	cfg := middleware.RateLimiterConfig{
		Store: middleware.NewRateLimiterMemoryStoreWithConfig(
			middleware.RateLimiterMemoryStoreConfig{
				Rate:      rate.Limit(s.cfg.RateLimitPerSec),
				Burst:     s.cfg.RateLimitBurst,
				ExpiresIn: 3 * time.Minute,
			},
		),
		IdentifierExtractor: func(c echo.Context) (string, error) {
			return c.RealIP(), nil
		},
		DenyHandler: func(c echo.Context, _ string, _ error) error {
			return c.NoContent(http.StatusTooManyRequests)
		},
	}
	return middleware.RateLimiterWithConfig(cfg)
}

// renderer adapts html/template to Echo's Renderer interface.
type renderer struct{ t *template.Template }

func (r *renderer) Render(w io.Writer, name string, data interface{}, _ echo.Context) error {
	return r.t.ExecuteTemplate(w, name, data)
}

// devRenderer re-parses templates from fsys on every Render call so edits to
// web/templates show up on the next request without rebuilding the binary.
// Selected when PIXELGO_TEMPLATES_DIR is set (see cmd/pixelgo/main.go), which
// swaps the embedded FS for an os.DirFS rooted at the live directory.
type devRenderer struct {
	fsys  fs.FS
	funcs template.FuncMap
}

func (r *devRenderer) Render(w io.Writer, name string, data interface{}, _ echo.Context) error {
	t, err := template.New("").Funcs(r.funcs).ParseFS(r.fsys, "*.html")
	if err != nil {
		return err
	}
	return t.ExecuteTemplate(w, name, data)
}

// humanCommas renders an int64 with thousands separators (e.g. 12,345).
func humanCommas(n int64) string {
	if n < 0 {
		return "-" + humanCommas(-n)
	}
	if n < 1000 {
		return itoa(n)
	}
	return humanCommas(n/1000) + "," + pad3(n%1000)
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func pad3(n int64) string {
	s := itoa(n)
	for len(s) < 3 {
		s = "0" + s
	}
	return s
}
