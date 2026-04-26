package api

import (
	"github.com/asilluron/pixelgo/internal/models"
	"github.com/labstack/echo/v4"
)

// meResponse is the payload for GET /v1/me. It echoes the caller's effective
// identity so clients can verify the token is valid and discover which org
// it's scoped to before making further calls.
type meResponse struct {
	KeyID    string            `json:"key_id"`
	KeyType  models.APIKeyType `json:"key_type"`
	KeyName  string            `json:"key_name"`
	OrgID    string            `json:"org_id"`
	UserID   string            `json:"user_id,omitempty"`
	IssuedAt string            `json:"issued_at"`
}

func (h *Handler) handleMe(c echo.Context) error {
	k := apiKey(c)
	return ok(c, meResponse{
		KeyID:    k.ID,
		KeyType:  k.Type,
		KeyName:  k.Name,
		OrgID:    orgID(c),
		UserID:   userID(c),
		IssuedAt: k.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	})
}
