package api

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/rs/zerolog"
)

// grantCallbackPath is the only path SpotiFLAC's local callback server
// exposes. Anything else is not a grant callback.
const grantCallbackPath = "/session-grant"

// UpstreamCBLookup resolves a verification state parameter to the callback
// URL the server itself recorded when it dispatched that challenge.
// Implemented by spotiflac.Client.
type UpstreamCBLookup interface {
	LookupUpstreamCB(state string) (string, bool)
}

// VerifyRelayHandler receives community verification callbacks from Byparr's
// headless browser after Turnstile is solved, then forwards the grant to
// SpotiFLAC's local callback server.
//
// This endpoint is deliberately unauthenticated: the remote verification
// service redirects a browser here and that redirect carries no API key. That
// makes validating the forwarding target the only thing standing between an
// anonymous caller and a server-side request forgery primitive, so the target
// is never simply taken from the request - see resolveUpstream.
type VerifyRelayHandler struct {
	lookup UpstreamCBLookup
	log    zerolog.Logger
}

// NewVerifyRelayHandler creates a handler for the /api/verify-relay endpoint.
// lookup may be nil, in which case only the strict-validation path applies.
func NewVerifyRelayHandler(lookup UpstreamCBLookup) *VerifyRelayHandler {
	return &VerifyRelayHandler{lookup: lookup, log: zerolog.Nop()}
}

func (h *VerifyRelayHandler) SetLogger(log zerolog.Logger) {
	h.log = log
}

// Handle processes GET /api/verify-relay?state=...&upstream_cb=...&grant=...
//
// upstream_cb previously went straight to http.Client.Get with no validation
// at all. Verified against production 2026-08-07: an unauthenticated caller
// could make this fetch http://127.0.0.1:8191/ (trawl, a different container
// sharing gluetun's network namespace) and https://api.monochrome.tf/ (the
// open internet, out through the household VPN), with up to 512 bytes of the
// response body reflected back. Everything in that namespace - aria2's RPC,
// kapowarr, trawl's MITM proxy, redis, gluetun's control API - was one
// anonymous GET away.
func (h *VerifyRelayHandler) Handle(c fiber.Ctx) error {
	grant := c.Query("grant")
	supplied := c.Query("upstream_cb")

	// This hop is logged explicitly because nothing else logs it. The route
	// is registered before api.RequestLogger is installed (deliberately, so
	// that Prometheus scrapes of /metrics do not fill the log), and fiber
	// does not apply middleware to routes registered before it - so a relay
	// callback left no trace whatsoever, success or failure.
	//
	// That cost real debugging time: with a solver reporting it had obtained
	// cf_clearance and a download reporting "community verification failed:
	// verification timed out", there was no way to tell whether the grant
	// ever arrived here. Every outcome below is logged now.
	h.log.Info().
		Bool("has_grant", grant != "").
		Bool("has_upstream_cb", supplied != "").
		Msg("verify relay: callback received")

	if grant == "" || supplied == "" {
		h.log.Warn().Msg("verify relay: callback missing grant or upstream_cb")
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "missing upstream_cb or grant parameter",
		})
	}

	// SpotiFLAC nests state inside upstream_cb rather than passing it
	// alongside (see Client.solveVerification), so accept either shape.
	state := c.Query("state")
	if state == "" {
		if u, err := url.Parse(supplied); err == nil {
			state = u.Query().Get("state")
		}
	}

	upstream, err := h.resolveUpstream(state, supplied)
	if err != nil {
		h.log.Warn().Err(err).Str("state", state).Msg("verify relay: rejected callback target")
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid or unknown callback target",
		})
	}

	forwardURL := appendGrant(upstream, grant)

	client := &http.Client{
		Timeout: 10 * time.Second,
		// The target is validated once, before the request. Following a
		// redirect would hand target selection back to the remote side and
		// undo that.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(forwardURL)
	if err != nil {
		h.log.Warn().Err(err).Msg("verify relay: forwarding grant failed")
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"error": "failed to forward grant to spotiflac callback",
		})
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// The upstream body is deliberately logged, never returned: echoing
		// it is what would turn this into a read primitive.
		h.log.Warn().Int("callback_code", resp.StatusCode).Msg("verify relay: callback rejected grant")
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"error": "spotiflac callback rejected grant",
		})
	}

	h.log.Info().Msg("verify relay: grant forwarded and accepted")
	return c.SendString("Verified")
}

// resolveUpstream decides where a grant may be forwarded.
//
// The server records state -> upstream_cb itself when it dispatches a
// challenge, so for a known state that recorded value is authoritative and
// any caller-supplied upstream_cb is ignored outright.
//
// The manual-browser relay flow records nothing (it never goes through
// Byparr), so an unrecorded state falls back to the caller's value - but only
// after it is checked against the one shape SpotiFLAC's callback server
// actually has: plain http, on loopback, at /session-grant. That leaves an
// anonymous caller able to reach nothing but a /session-grant path on
// localhost, which no other service implements.
func (h *VerifyRelayHandler) resolveUpstream(state, supplied string) (string, error) {
	if h.lookup != nil && state != "" {
		if recorded, ok := h.lookup.LookupUpstreamCB(state); ok {
			return recorded, nil
		}
	}
	if err := validateCallbackURL(supplied); err != nil {
		return "", err
	}
	return supplied, nil
}

func validateCallbackURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("unparseable upstream_cb: %w", err)
	}
	if u.Scheme != "http" {
		return fmt.Errorf("upstream_cb scheme %q is not http", u.Scheme)
	}
	if u.Path != grantCallbackPath {
		return fmt.Errorf("upstream_cb path %q is not %s", u.Path, grantCallbackPath)
	}
	host := u.Hostname()
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("upstream_cb host %q is not loopback", host)
	}
	return nil
}

func appendGrant(upstream, grant string) string {
	sep := "?"
	if u, err := url.Parse(upstream); err == nil && u.RawQuery != "" {
		sep = "&"
	}
	return upstream + sep + "grant=" + url.QueryEscape(grant)
}
