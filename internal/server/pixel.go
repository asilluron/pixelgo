package server

import (
	"context"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
)

// gif1x1 is a 43-byte transparent 1x1 GIF89a. Serving it from memory avoids
// any filesystem I/O on the hot path.
var gif1x1 = []byte{
	0x47, 0x49, 0x46, 0x38, 0x39, 0x61, 0x01, 0x00, 0x01, 0x00,
	0x80, 0x00, 0x00, 0x00, 0x00, 0x00, 0xff, 0xff, 0xff, 0x21,
	0xf9, 0x04, 0x01, 0x00, 0x00, 0x00, 0x00, 0x2c, 0x00, 0x00,
	0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0x02, 0x02, 0x44,
	0x01, 0x00, 0x3b,
}

// handlePixel is the hot path. It responds with a 1x1 GIF immediately and
// fires the counter increment asynchronously so client latency is unaffected
// by Redis round-trip time.
func (s *Server) handlePixel(c echo.Context) error {
	id := c.Param("id")

	// Fire-and-forget increment. We detach the request context so cancellation
	// on client disconnect doesn't drop the write. Caps the write at 2s to
	// avoid goroutine leaks if Redis is unreachable. The pipelined IncrPixel
	// bumps lifetime + daily + hourly buckets in a single round-trip.
	if id != "" {
		now := time.Now()
		go func(pixelID string) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_, _ = s.pixels.IncrPixel(ctx, pixelID, now)
		}(id)
	}

	// Aggressive no-cache so every page load re-requests the pixel.
	h := c.Response().Header()
	h.Set(echo.HeaderContentType, "image/gif")
	h.Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0, private")
	h.Set("Pragma", "no-cache")
	h.Set("Expires", "0")

	return c.Blob(http.StatusOK, "image/gif", gif1x1)
}
