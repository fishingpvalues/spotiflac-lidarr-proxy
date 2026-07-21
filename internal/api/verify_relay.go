package api

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gofiber/fiber/v3"
)

// VerificationStateStore is the interface the verify-relay handler uses to
// look up the upstream_cb URL for a given state parameter.
type VerificationStateStore interface {
	LookupUpstreamCB(state string) (string, bool)
}

// VerifyRelayHandler receives community verification callbacks from Byparr's
// headless browser after Turnstile is solved, then forwards the grant to
// SpotiFLAC's local callback server.
type VerifyRelayHandler struct {
	store VerificationStateStore
}

// NewVerifyRelayHandler creates a handler for the /api/verify-relay endpoint.
func NewVerifyRelayHandler(store VerificationStateStore) *VerifyRelayHandler {
	return &VerifyRelayHandler{store: store}
}

// Handle processes GET /api/verify-relay?state=...&grant=...
// Called by Byparr's browser after solving Turnstile — the challenge page
// redirects to cb= which we rewrote to point at this endpoint.
func (h *VerifyRelayHandler) Handle(c fiber.Ctx) error {
	state := c.Query("state")
	grant := c.Query("grant")

	if state == "" || grant == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "missing state or grant parameter",
		})
	}

	upstreamCB, ok := h.store.LookupUpstreamCB(state)
	if !ok {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "unknown verification state",
		})
	}

	// Forward the grant to SpotiFLAC's local callback server.
	// upstream_cb looks like: http://127.0.0.1:<random_port>/session-grant
	forwardURL := fmt.Sprintf("%s?state=%s&grant=%s", upstreamCB, state, grant)

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
			"error":        "spotiflac callback rejected grant",
			"callback_code": resp.StatusCode,
			"callback_body": string(body),
		})
	}

	return c.SendString("Verified")
}
