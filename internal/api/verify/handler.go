// Package verify relays Tidal/Qobuz/Amazon's one-time community-verification
// challenge (see spotiflac-cli's openCommunityVerificationRelay) from a real
// person's real browser back to the loopback listener spotiflac-cli is
// waiting on on this same host. It does not participate in or alter the
// challenge itself in any way - a real human still has to complete the
// actual verification in their own browser exactly as spotiflac-cli's
// desktop GUI already requires. This only bridges the network gap between
// that loopback listener and a browser on a different machine.
package verify

import (
	"net/http"
	"net/url"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/rs/zerolog"
)

type Handler struct {
	store *Store
	log   zerolog.Logger
}

func NewHandler(store *Store) *Handler {
	return &Handler{store: store, log: zerolog.Nop()}
}

func (h *Handler) SetLogger(log zerolog.Logger) {
	h.log = log
}

// RegisterRoutes mounts this at the top level, deliberately outside "/api":
// the /api group's auth middleware matches by path prefix (see main.go), and
// the remote verification service's redirect here carries no API key -
// only "upstream_cb" and "grant".
func (h *Handler) RegisterRoutes(app *fiber.App) {
	app.Get("/verify/callback", h.handleCallback)
}

func (h *Handler) handleCallback(c fiber.Ctx) error {
	upstreamCB := c.Query("upstream_cb")
	grant := c.Query("grant")
	if upstreamCB == "" || grant == "" {
		return c.Status(fiber.StatusBadRequest).SendString("missing upstream_cb or grant")
	}

	// This must relay a grant only to the exact loopback URL spotiflac-cli
	// itself reported expecting one for - never to whatever an inbound
	// request merely claims upstream_cb is. Without this check, "is it a
	// loopback address" alone still lets any caller make this proxy issue
	// requests to an arbitrary host:port on its own loopback, which in
	// production shares a network namespace with the gluetun sidecar (see
	// network_mode: service:gluetun).
	expected, pending := h.store.ExpectedCB()
	if !pending {
		return c.Status(fiber.StatusGone).SendString("no verification is currently pending")
	}
	if upstreamCB != expected {
		h.log.Warn().Str("got", upstreamCB).Msg("rejected verify callback: upstream_cb did not match the pending verification's recorded callback")
		return c.Status(fiber.StatusBadRequest).SendString("upstream_cb does not match the pending verification")
	}

	target, err := url.Parse(expected)
	if err != nil {
		h.log.Error().Err(err).Msg("stored expected callback is not a valid URL")
		return c.Status(fiber.StatusInternalServerError).SendString("internal error")
	}
	q := target.Query()
	q.Set("grant", grant)
	target.RawQuery = q.Encode()

	h.store.Clear()

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(target.String())
	if err != nil {
		h.log.Error().Err(err).Msg("verify callback relay failed")
		return c.Status(fiber.StatusBadGateway).SendString("failed to reach the local verification listener - it may have already timed out")
	}
	defer resp.Body.Close()

	// The upstream body isn't relayed here even though it's now known to
	// come from our own trusted local listener - keep this endpoint from
	// ever becoming a vector for reflecting arbitrary loopback content back
	// out to the internet, on principle.
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return c.Status(fiber.StatusOK).SendString("Verified. You can close this window.")
	}
	return c.Status(fiber.StatusBadGateway).SendString("local verification listener reported an error")
}
