package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/api"
)

// The skiplist used to test every skip value against BOTH the sabnzbd "mode"
// parameter and the newznab "t" parameter. Since the group mounted at the
// bare "/api" prefix has to carry newznab's "caps" in its own skiplist (see
// TestAPIKeyAuthWithSkiplistOnOverlappingGroupsExemptsSharedSkipMode),
// appending &t=caps to any mode= request skipped authentication outright.
//
// Verified against production 2026-08-07: GET /api?mode=queue returned 401,
// GET /api?mode=queue&t=caps returned the full queue. The same trick reached
// mode=get_config, mode=addurl and the queue-delete action with no
// credentials at all - the whole SABnzbd surface, read and write, from any
// container that could route to the port.
//
// A skip value must only exempt the parameter that actually dispatches the
// request: "caps" is a newznab type, and a request carrying mode= is not a
// newznab request no matter what else it carries.
func TestSkipValueForOneParamCannotExemptTheOther(t *testing.T) {
	app := fiber.New()
	g := app.Group("/api")
	g.Use(api.APIKeyAuth("correct-key", []string{"version", "auth"}, []string{"caps"}))
	g.Get("/", func(c fiber.Ctx) error { return c.SendString("ok") })
	g.Post("/", func(c fiber.Ctx) error { return c.SendString("ok") })

	for _, tc := range []struct {
		name   string
		method string
		target string
	}{
		{"read via smuggled t=caps", "GET", "/api?mode=queue&output=json&t=caps"},
		{"config dump via smuggled t=version", "GET", "/api?mode=get_config&t=version"},
		{"write via smuggled t=caps", "POST", "/api?mode=addurl&name=https://open.spotify.com/track/x&t=caps"},
		{"delete via smuggled t=caps", "GET", "/api?mode=queue&name=delete&value=x&t=caps"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(tc.method, tc.target, nil)
			resp, err := app.Test(req)
			require.NoError(t, err)
			assert.Equal(t, 401, resp.StatusCode,
				"smuggling a newznab type into a sabnzbd request must not skip auth")
		})
	}
}

// The mirror image: a sabnzbd mode must not exempt a newznab request.
func TestSabnzbdModeCannotExemptNewznabRequest(t *testing.T) {
	app := fiber.New()
	g := app.Group("/api/newznab")
	g.Use(api.APIKeyAuth("correct-key", nil, []string{"caps"}))
	g.Get("/", func(c fiber.Ctx) error { return c.SendString("ok") })

	req, _ := http.NewRequest("GET", "/api/newznab?t=music&artist=x&mode=caps", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 401, resp.StatusCode)
}

// A POSTed form mode must be judged too - the middleware reads mode from the
// query first and the body second, exactly as sabnzbd's dispatch does, so the
// two can never disagree about which mode is being invoked.
func TestFormBodyModeIsAuthenticated(t *testing.T) {
	app := fiber.New()
	g := app.Group("/api")
	g.Use(api.APIKeyAuth("correct-key", []string{"version", "auth"}, []string{"caps"}))
	g.Post("/", func(c fiber.Ctx) error { return c.SendString("ok") })

	req, _ := http.NewRequest("POST", "/api?t=caps", strings.NewReader("mode=addurl&name=x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 401, resp.StatusCode)
}

// The legitimate exemptions must keep working, or Lidarr cannot add or test
// the client at all.
func TestLegitimateExemptionsStillPass(t *testing.T) {
	app := fiber.New()
	root := app.Group("/api")
	root.Use(api.APIKeyAuth("correct-key", []string{"version", "auth"}, []string{"caps"}))
	root.Get("/", func(c fiber.Ctx) error { return c.SendString("ok") })
	nznb := app.Group("/api/newznab")
	nznb.Use(api.APIKeyAuth("correct-key", nil, []string{"caps"}))
	nznb.Get("/", func(c fiber.Ctx) error { return c.SendString("ok") })

	for _, target := range []string{
		"/api?mode=version",
		"/api?mode=auth",
		"/api/newznab?t=caps",
		"/api?mode=queue&apikey=correct-key",
		"/api/newznab?t=music&artist=x&apikey=correct-key",
	} {
		req, _ := http.NewRequest("GET", target, nil)
		resp, err := app.Test(req)
		require.NoError(t, err)
		assert.Equal(t, 200, resp.StatusCode, "%s must still be allowed", target)
	}
}
