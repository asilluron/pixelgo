package api

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// Every v1 response is wrapped. Success bodies carry a `data` field and,
// optionally, a `meta` field (pagination, generated_at, etc). Error bodies
// carry an `error` field with a stable machine-readable code, a human
// message, and optional structured details. Clients can always branch on
// the presence of `error` vs `data`.
//
//	{ "data": {...}, "meta": {...} }
//	{ "error": { "code": "not_found", "message": "...", "details": {...} } }

// Success is the success envelope.
type Success struct {
	Data any `json:"data"`
	Meta any `json:"meta,omitempty"`
}

// Error is the error envelope body.
type Error struct {
	Err ErrorBody `json:"error"`
}

// ErrorBody describes a single error. Code is a short, stable identifier
// (e.g. "unauthorized", "not_found", "invalid_request") — clients should
// branch on it rather than the HTTP status or human message.
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

// Common error codes.
const (
	CodeUnauthorized   = "unauthorized"
	CodeForbidden      = "forbidden"
	CodeNotFound       = "not_found"
	CodeInvalidRequest = "invalid_request"
	CodeInternal       = "internal_error"
)

// ok writes a 200 with a data envelope and optional meta.
func ok(c echo.Context, data any, meta ...any) error {
	resp := Success{Data: data}
	if len(meta) > 0 {
		resp.Meta = meta[0]
	}
	return c.JSON(http.StatusOK, resp)
}

// errJSON writes an error envelope with the given status code.
func errJSON(c echo.Context, status int, code, message string, details ...any) error {
	body := Error{Err: ErrorBody{Code: code, Message: message}}
	if len(details) > 0 {
		body.Err.Details = details[0]
	}
	return c.JSON(status, body)
}

// unauthorized, forbidden, notFound, badRequest, internal are thin wrappers
// around errJSON so handlers stay terse and the codes stay consistent.
func unauthorized(c echo.Context, msg string) error {
	if msg == "" {
		msg = "missing or invalid credentials"
	}
	return errJSON(c, http.StatusUnauthorized, CodeUnauthorized, msg)
}

func forbidden(c echo.Context, msg string) error {
	if msg == "" {
		msg = "not allowed"
	}
	return errJSON(c, http.StatusForbidden, CodeForbidden, msg)
}

func notFound(c echo.Context, msg string) error {
	if msg == "" {
		msg = "not found"
	}
	return errJSON(c, http.StatusNotFound, CodeNotFound, msg)
}

func badRequest(c echo.Context, msg string) error {
	return errJSON(c, http.StatusBadRequest, CodeInvalidRequest, msg)
}

func internal(c echo.Context, err error) error {
	// Deliberately opaque — do not leak internal error text to API callers.
	// Servers that want to trace a specific failure should correlate by the
	// request-id header, which the Echo logger already emits.
	c.Logger().Errorf("api internal error: %v", err)
	return errJSON(c, http.StatusInternalServerError, CodeInternal, "internal error")
}
