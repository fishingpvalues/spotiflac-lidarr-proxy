package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The failure this file exists to prevent is the one that destroyed trust in
// the last tool to occupy this slot: an endpoint reachable without the API key
// that answers with configuration. A proxy like this holds a Lidarr-adjacent
// key and is routinely put on a LAN, so "which routes are open" has to be a
// decision someone made on purpose, not a property nobody measured.
//
// These tests run against the REAL route table - buildApp, the same function
// runServe calls - rather than an app assembled in the test. A test that
// builds its own routes proves nothing about the server that ships.

// openRoute documents one deliberately unauthenticated entry point.
type openRoute struct {
	method string
	path   string
	query  string
	why    string
}

// Every route that answers without an API key, and the reason. Adding a route
// to the server without adding it here fails TestEveryRouteIsAccountedFor.
var openRoutes = []openRoute{
	{"GET", "/health", "", "container healthcheck; runs before any key is configured"},
	{"GET", "/api/verify-relay", "", "a browser redirect carries no key; it forwards only to listeners this process dispatched"},
	{"GET", "/verify/callback", "", "same, for the remote verification service's redirect"},
	{"GET", "/api/sabnzbd/", "mode=version", "Lidarr probes the version before a key is configured"},
	{"GET", "/api/sabnzbd/", "mode=auth", "Lidarr probes the auth mode before a key is configured"},
	{"GET", "/api/", "mode=version", "same, on the urlBase-compatible mount"},
	{"GET", "/api/", "mode=auth", "same, on the urlBase-compatible mount"},
	{"GET", "/api/", "t=caps", "Lidarr reads Newznab caps before a key is configured"},
	{"GET", "/api/newznab/", "t=caps", "same, on the Newznab mount"},
}

// Routes that must refuse an unauthenticated request outright.
var closedRoutes = []openRoute{
	{"GET", "/metrics", "", "SPF_METRICS_REQUIRE_AUTH defaults to true"},
	{"GET", "/api/sabnzbd/", "mode=queue", "the queue lists what this instance is downloading"},
	{"GET", "/api/sabnzbd/", "mode=history", "history lists what it downloaded"},
	{"GET", "/api/sabnzbd/", "mode=get_config", "get_config reports the output directory"},
	{"GET", "/api/sabnzbd/", "mode=fullstatus", ""},
	{"GET", "/api/sabnzbd/", "mode=get_cats", ""},
	{"GET", "/api/", "mode=queue", ""},
	{"GET", "/api/", "mode=get_config", ""},
	{"GET", "/api/newznab/", "t=search&q=x", "a search is a live call to an upstream provider"},
	{"GET", "/api/newznab/", "t=music&q=x", ""},
	{"GET", "/api/newznab/", "", "no t= at all must not fall through to open"},
	{"POST", "/api/sabnzbd/", "mode=addurl", "addurl starts a download"},
	{"POST", "/api/", "mode=addurl", ""},
	// The exemptions belong to one parameter each. Pairing an exempt value
	// with the other parameter's name must not carry the exemption across:
	// a shared skiplist once let &t=caps skip auth on every mode= request.
	{"GET", "/api/sabnzbd/", "mode=caps", "caps is a t= exemption, not a mode= one"},
	{"GET", "/api/newznab/", "t=version", "version is a mode= exemption, not a t= one"},
	{"GET", "/api/newznab/", "t=auth", "auth is a mode= exemption, not a t= one"},
	{"GET", "/api/", "mode=queue&t=caps", "an exempt t= must not open a protected mode="},
	{"GET", "/api/", "mode=addurl&t=caps", ""},
}

func do(t *testing.T, method, path, query string) (int, string) {
	t.Helper()
	app, _ := testApp(t)
	url := path
	if query != "" {
		url += "?" + query
	}
	req := httptest.NewRequest(method, url, nil)
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 15 * time.Second})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(body)
}

// Every route the server registers must be listed above as open or closed.
// A new route that nobody classified fails here rather than shipping.
func TestEveryRouteIsAccountedFor(t *testing.T) {
	app, _ := testApp(t)

	classified := map[string]bool{}
	for _, r := range append(append([]openRoute{}, openRoutes...), closedRoutes...) {
		classified[r.method+" "+r.path] = true
	}

	for _, route := range app.GetRoutes(true) {
		if route.Method == http.MethodHead || route.Method == http.MethodOptions {
			continue
		}
		key := route.Method + " " + route.Path
		assert.True(t, classified[key],
			"route %s is registered but classified neither open nor closed in auth_coverage_test.go - "+
				"decide whether it requires the API key and say so there", key)
	}
}

func TestClosedRoutesRefuseAnUnauthenticatedRequest(t *testing.T) {
	for _, r := range closedRoutes {
		t.Run(r.method+" "+r.path+"?"+r.query, func(t *testing.T) {
			code, body := do(t, r.method, r.path, r.query)
			assert.Equal(t, http.StatusUnauthorized, code,
				"%s %s?%s answered %d without an API key. %s", r.method, r.path, r.query, code, r.why)
			assert.NotContains(t, body, testAPIKey)
		})
	}
}

func TestOpenRoutesAreOpenOnPurposeAndLeakNothing(t *testing.T) {
	for _, r := range openRoutes {
		t.Run(r.method+" "+r.path+"?"+r.query, func(t *testing.T) {
			code, body := do(t, r.method, r.path, r.query)
			assert.NotEqual(t, http.StatusUnauthorized, code,
				"%s %s?%s is documented open (%s) but answered 401", r.method, r.path, r.query, r.why)

			// The Huntarr failure, as a test: no endpoint that answers
			// without a key may put the key in its response.
			assert.NotContains(t, body, testAPIKey,
				"%s %s?%s returned the API key to an unauthenticated caller", r.method, r.path, r.query)
			// Nor any obvious redaction-miss of it.
			assert.NotContains(t, strings.ToLower(body), strings.ToLower(testAPIKey))
		})
	}
}

// A wrong key must be refused exactly like no key. Anything that answers one
// but not the other is comparing keys in a way that leaks.
func TestAWrongKeyIsRefusedLikeNoKey(t *testing.T) {
	for _, r := range closedRoutes {
		q := r.query
		if q != "" {
			q += "&"
		}
		t.Run(r.method+" "+r.path+"?"+r.query, func(t *testing.T) {
			code, _ := do(t, r.method, r.path, q+"apikey=not-the-key")
			assert.Equal(t, http.StatusUnauthorized, code)
		})
	}
}
