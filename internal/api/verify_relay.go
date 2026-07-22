package api

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
)

// VerifyRelayHandler receives community verification callbacks from Byparr's
// headless browser after Turnstile is solved, then forwards the grant to
// SpotiFLAC's local callback server.
type VerifyRelayHandler struct{}

// NewVerifyRelayHandler creates a handler for the /api/verify-relay endpoint.
func NewVerifyRelayHandler() *VerifyRelayHandler {
	return &VerifyRelayHandler{}
}

// Handle processes GET /api/verify-relay?upstream_cb=...&grant=...
// Called by Byparr's browser after solving Turnstile — the challenge page
// redirects to cb= which we rewrote to point at this endpoint.
// upstream_cb is embedded in cb by SpotiFLAC's relay mechanism and passed
// through as a query parameter in the redirect URL.
func (h *VerifyRelayHandler) Handle(c fiber.Ctx) error {
	upstreamCB := c.Query("upstream_cb")
	grant := c.Query("grant")

	if upstreamCB == "" || grant == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "missing upstream_cb or grant parameter",
		})
	}

	// Forward the grant to SpotiFLAC's local callback server.
	// upstream_cb looks like: http://127.0.0.1:<random_port>/session-grant?state=...
	// Append &grant=... to forward the grant.
	sep := "?"
	if strings.Contains(upstreamCB, "?") {
		sep = "&"
	}
	forwardURL := fmt.Sprintf("%s%sgrant=%s", upstreamCB, sep, grant)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(forwardURL)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"error": "failed to forward grant to spotiflac callback",
		})
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"error":         "spotiflac callback rejected grant",
			"callback_code": resp.StatusCode,
			"callback_body": string(body),
		})
	}

	return c.SendString("Verified")
}
