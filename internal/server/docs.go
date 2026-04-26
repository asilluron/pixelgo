package server

import (
	"net/http"

	apispec "github.com/asilluron/pixelgo/api"
	"github.com/labstack/echo/v4"
)

// handleOpenAPISpec serves the embedded YAML spec for tooling consumption
// (Swagger UI loads it via XHR, `redocly lint` expects a raw GET).
func (s *Server) handleOpenAPISpec(c echo.Context) error {
	return c.Blob(http.StatusOK, apispec.ContentType, apispec.Spec)
}

// handleDocs renders a Swagger UI page pointed at /openapi.yaml. Pulling the
// UI from a CDN keeps the binary small and avoids a JS build step; if that
// offends operators, it's a 5-line swap for a vendored bundle.
func (s *Server) handleDocs(c echo.Context) error {
	return c.HTML(http.StatusOK, swaggerHTML)
}

// swaggerHTML is a minimal, unstyled Swagger UI shell. The version is pinned
// so a CDN rotation can't break customers mid-demo; bump it deliberately.
const swaggerHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>pixelgo API · docs</title>
  <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.17.14/swagger-ui.css" />
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.17.14/swagger-ui-bundle.js" crossorigin></script>
  <script>
    window.addEventListener('load', () => {
      window.ui = SwaggerUIBundle({
        url: '/openapi.yaml',
        dom_id: '#swagger-ui',
        deepLinking: true,
        displayRequestDuration: true,
        tryItOutEnabled: true,
        persistAuthorization: true,
      });
    });
  </script>
</body>
</html>`
