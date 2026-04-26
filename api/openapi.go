// Package apispec embeds the hand-written OpenAPI 3 description of pixelgo's
// public JSON API. It is served verbatim at /openapi.yaml and rendered by
// Swagger UI at /docs; there is no code generation step.
package apispec

import _ "embed"

//go:embed openapi.yaml
var Spec []byte

// ContentType is the MIME type used when serving the spec over HTTP.
// RFC 9512 is the standard, but OpenAPI tooling still overwhelmingly expects
// application/yaml, which is what Swagger UI and Redocly both request.
const ContentType = "application/yaml"
