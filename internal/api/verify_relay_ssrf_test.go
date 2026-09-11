package api_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/api"
)

// stubLookup stands in for spotiflac.Client's state -> upstream_cb map.
type stubLookup map[string]string

func (s stubLookup) LookupUpstreamCB(state string) (string, bool) {
	v, ok := s[state]
	return v, ok
}

// IsRecordedUpstreamCB mirrors the client's: match on host:port and path,
// because a callback returns through a browser redirect and its query string
// can be re-encoded on the way.
func (s stubLookup) IsRecordedUpstreamCB(cb string) bool {
	want, err := url.Parse(cb)
	if err != nil {
		return false
	}
	for _, recorded := range s {
		got, err := url.Parse(recorded)
		if err != nil {
			continue
		}
		if got.Host == want.Host && got.Path == want.Path {
			return true
		}
	}
	return false
}

func relayApp(lookup api.UpstreamCBLookup) *fiber.App {
	app := fiber.New()
	h := api.NewVerifyRelayHandler(lookup)
	app.Get("/api/verify-relay", h.Handle)
	return app
}

func get(t *testing.T, app *fiber.App, target string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", target, nil)
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 0})
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// /api/verify-relay is deliberately unauthenticated - the remote verification
// service redirects a browser here and that redirect carries no API key. It
// then took upstream_cb straight from the query string and fetched it with no
// validation whatsoever, which made it an unauthenticated server-side request
// forgery primitive.
//
// Verified against production 2026-08-07: with no credentials it fetched
// http://127.0.0.1:8191/ (trawl, a different container sharing gluetun's
// network namespace) and https://api.monochrome.tf/ (the open internet, out
// through the household VPN), and reflected up to 512 bytes of the response
// body back to the caller. Everything in that namespace - aria2's RPC,
// kapowarr, trawl's MITM proxy, redis, gluetun's own control API - was one
// unauthenticated GET away.
func TestVerifyRelayRefusesArbitraryUpstream(t *testing.T) {
	app := relayApp(stubLookup{})

	// Deliberately not httptest servers: those bind loopback, which is the
	// one host the fallback path is allowed to reach. These are the shapes
	// that must never be fetched at all.
	for _, tc := range []struct{ name, upstream string }{
		{"external host by name", "http://evil.example/session-grant"},
		{"external https", "https://example.com/session-grant"},
		{"private lan address", "http://192.168.178.93/session-grant"},
		{"docker bridge neighbor", "http://172.22.0.5/session-grant"},
		{"link local metadata", "http://169.254.169.254/session-grant"},
		{"loopback but not a grant path", "http://127.0.0.1:8191/"},
		{"loopback gluetun control api", "http://127.0.0.1:8008/v1/publicip/ip"},
		{"non-http scheme", "file:///etc/passwd"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _ := get(t, app,
				"/api/verify-relay?state=s1&grant=g&upstream_cb="+url.QueryEscape(tc.upstream))
			assert.Equal(t, 400, code, "unvalidated upstream must be refused outright")
		})
	}
}

// The server records state -> upstream_cb itself when it dispatches a
// challenge. That recorded value is authoritative; a caller-supplied
// upstream_cb for a known state must be ignored, not merged or preferred.
func TestVerifyRelayPrefersRecordedUpstreamOverQuery(t *testing.T) {
	var mu sync.Mutex
	var gotGrant string
	real := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotGrant = r.URL.Query().Get("grant")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer real.Close()

	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("relay followed the caller-supplied upstream_cb instead of the recorded one")
		w.WriteHeader(http.StatusOK)
	}))
	defer attacker.Close()

	app := relayApp(stubLookup{"s1": real.URL + "/session-grant?state=s1"})

	code, _ := get(t, app,
		"/api/verify-relay?state=s1&grant=g123&upstream_cb="+url.QueryEscape(attacker.URL+"/session-grant"))
	assert.Equal(t, 200, code)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "g123", gotGrant, "grant must be forwarded to the recorded callback")
}

// The upstream's response body must never be echoed to the caller: that is
// what turns a blind SSRF into a read primitive.
func TestVerifyRelayNeverReflectsUpstreamBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("SECRET-INTERNAL-DATA"))
	}))
	defer upstream.Close()

	app := relayApp(stubLookup{"s1": upstream.URL + "/session-grant"})

	code, body := get(t, app, "/api/verify-relay?state=s1&grant=g")
	assert.NotEqual(t, 200, code)
	assert.NotContains(t, body, "SECRET-INTERNAL-DATA")
	assert.NotContains(t, strings.ToLower(body), "callback_body")
}

func TestVerifyRelayRequiresUpstreamAndGrant(t *testing.T) {
	app := relayApp(stubLookup{})
	for _, target := range []string{
		"/api/verify-relay",
		"/api/verify-relay?upstream_cb=http://127.0.0.1:9/session-grant",
		"/api/verify-relay?grant=g",
	} {
		code, _ := get(t, app, target)
		assert.Equal(t, 400, code, "%s must be refused", target)
	}
}

// The shape SpotiFLAC actually produces: state is nested inside upstream_cb,
// not passed alongside it. The recorded mapping still has to win.
func TestVerifyRelayResolvesStateNestedInUpstreamCB(t *testing.T) {
	var mu sync.Mutex
	var gotGrant string
	real := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotGrant = r.URL.Query().Get("grant")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer real.Close()

	app := relayApp(stubLookup{"abc123": real.URL + "/session-grant?state=abc123"})

	// Attacker points the host elsewhere but keeps a state we have recorded.
	code, _ := get(t, app, "/api/verify-relay?grant=g9&upstream_cb="+
		url.QueryEscape("http://evil.example/session-grant?state=abc123"))
	assert.Equal(t, 200, code)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "g9", gotGrant, "recorded callback must win over the supplied host")
}

// The genuine loopback callback with no recorded state (the manual browser
// relay flow, which never goes through Byparr) must still work.
// The fallback path - a callback arriving without a state this process
// recognizes - is restricted to listeners this process actually dispatched a
// verification to. "Is it loopback" was the old rule and it is not enough
// here: this container shares gluetun's network namespace with qBittorrent,
// slskd, aria2, kapowarr, searxng and i2pd, so an attacker-chosen port on an
// unauthenticated endpoint is a port-existence oracle across all of them,
// answerable forever on an idle instance.
func TestVerifyRelayFallbackRequiresARecordedListener(t *testing.T) {
	// Nothing dispatched: the endpoint forwards nowhere, whatever is asked.
	idle := relayApp(stubLookup{})
	for _, target := range []string{
		"http://127.0.0.1:39637/session-grant?state=x",
		"http://localhost:39637/session-grant",
		"http://127.0.0.1:8282/session-grant", // qBittorrent, same namespace
		"http://127.0.0.1:6379/session-grant", // redis, same namespace
	} {
		code, _ := get(t, idle, "/api/verify-relay?grant=g&upstream_cb="+url.QueryEscape(target))
		assert.Equal(t, 400, code, "idle instance must not forward to %s", target)
	}

	// One verification dispatched to :39637. That listener is now reachable
	// through the fallback; its neighbors still are not.
	busy := relayApp(stubLookup{"recorded-state": "http://127.0.0.1:39637/session-grant?state=recorded-state"})

	code, _ := get(t, busy, "/api/verify-relay?grant=g&upstream_cb="+
		url.QueryEscape("http://127.0.0.1:39637/session-grant?state=unknown"))
	assert.NotEqual(t, 400, code, "a listener this process dispatched to must be reachable without a known state")

	for _, neighbor := range []string{
		"http://127.0.0.1:8282/session-grant",
		"http://127.0.0.1:8191/session-grant",
	} {
		code, _ := get(t, busy, "/api/verify-relay?grant=g&upstream_cb="+url.QueryEscape(neighbor))
		assert.Equal(t, 400, code, "port %s was never dispatched to and must stay unreachable", neighbor)
	}
}

// The shape checks still apply before the recorded-listener check, so a
// non-loopback or wrong-path target is refused even while a verification is
// pending.
func TestVerifyRelayFallbackStillEnforcesShape(t *testing.T) {
	app := relayApp(stubLookup{"s": "http://127.0.0.1:39637/session-grant"})
	for _, bad := range []string{
		"http://127.0.0.1:39637/",              // wrong path
		"https://example.com/session-grant",    // not loopback
		"http://10.0.0.5/session-grant",        // not loopback
		"http://127.0.0.1:39637/../etc/passwd", // path traversal
	} {
		code, _ := get(t, app, "/api/verify-relay?grant=g&upstream_cb="+url.QueryEscape(bad))
		assert.Equal(t, 400, code, "must refuse %s", bad)
	}
}

// TestVerifyRelayLogsEveryOutcome guards the observability of the one hop
// that had none. The route is registered before api.RequestLogger is
// installed, and fiber does not apply middleware to earlier routes, so a
// relay callback left no trace at all - which made "did the grant ever
// arrive?" unanswerable while debugging a solver that reported success and a
// download that reported "verification timed out".
func TestVerifyRelayLogsEveryOutcome(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
		want  string
	}{
		{"missing params", "", "callback missing grant or upstream_cb"},
		{
			"rejected target",
			"?grant=g&upstream_cb=" + url.QueryEscape("http://10.0.0.1/session-grant"),
			"rejected callback target",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			h := api.NewVerifyRelayHandler(nil)
			h.SetLogger(zerolog.New(&buf))

			app := fiber.New()
			app.Get("/api/verify-relay", h.Handle)

			req, _ := http.NewRequest("GET", "/api/verify-relay"+tc.query, nil)
			_, err := app.Test(req)
			require.NoError(t, err)

			logged := buf.String()
			assert.Contains(t, logged, "callback received",
				"every callback must be recorded, whatever happens to it")
			assert.Contains(t, logged, tc.want)
		})
	}
}
