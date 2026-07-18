package api_test

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/api"
)

func TestAPIKeyAuthWithSkiplistAcceptsCorrectKey(t *testing.T) {
	app := fiber.New()
	app.Use(api.APIKeyAuthWithSkiplist("correct-key"))
	app.Get("/", func(c fiber.Ctx) error { return c.SendString("ok") })

	req, _ := http.NewRequest("GET", "/?apikey=correct-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
}

func TestAPIKeyAuthWithSkiplistRejectsWrongKey(t *testing.T) {
	app := fiber.New()
	app.Use(api.APIKeyAuthWithSkiplist("correct-key"))
	app.Get("/", func(c fiber.Ctx) error { return c.SendString("ok") })

	req, _ := http.NewRequest("GET", "/?apikey=wrong-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 401, resp.StatusCode)
}

func TestAPIKeyAuthWithSkiplistRejectsDifferentLengthKey(t *testing.T) {
	// Regression guard: subtle.ConstantTimeCompare requires equal-length
	// inputs; a naive switch to it would panic or misbehave on length
	// mismatch if not handled explicitly.
	app := fiber.New()
	app.Use(api.APIKeyAuthWithSkiplist("a-fairly-long-correct-key"))
	app.Get("/", func(c fiber.Ctx) error { return c.SendString("ok") })

	req, _ := http.NewRequest("GET", "/?apikey=short", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 401, resp.StatusCode)
}

func TestRequestLoggerRedactsAPIKey(t *testing.T) {
	var buf bytes.Buffer
	log := zerolog.New(&buf)

	app := fiber.New()
	app.Use(api.RequestLogger(log))
	app.Get("/", func(c fiber.Ctx) error { return c.SendString("ok") })

	req, _ := http.NewRequest("GET", "/?mode=queue&apikey=super-secret-value", nil)
	_, err := app.Test(req)
	require.NoError(t, err)

	assert.NotContains(t, buf.String(), "super-secret-value")
	assert.Contains(t, buf.String(), "apikey=***")
}

func TestRequestLoggerRedactsAPIKeyEvenWithMalformedUnrelatedParam(t *testing.T) {
	// Regression guard for the fail-open leak: url.ParseQuery returns an
	// error if ANY component of the query string fails to unescape (e.g.
	// a stray %zz), even when apikey itself parsed fine. The old
	// implementation fell back to returning the raw, unredacted query
	// string in that case, leaking the secret in cleartext.
	var buf bytes.Buffer
	log := zerolog.New(&buf)

	app := fiber.New()
	app.Use(api.RequestLogger(log))
	app.Get("/", func(c fiber.Ctx) error { return c.SendString("ok") })

	req, _ := http.NewRequest("GET", "/?apikey=REALSECRET&x=%zz", nil)
	_, err := app.Test(req)
	require.NoError(t, err)

	assert.NotContains(t, buf.String(), "REALSECRET")
	assert.Contains(t, buf.String(), "apikey=***")
}

func TestRequestLoggerHandlesEmptyQueryString(t *testing.T) {
	var buf bytes.Buffer
	log := zerolog.New(&buf)

	app := fiber.New()
	app.Use(api.RequestLogger(log))
	app.Get("/", func(c fiber.Ctx) error { return c.SendString("ok") })

	req, _ := http.NewRequest("GET", "/", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)

	assert.Equal(t, 200, resp.StatusCode)
	assert.Contains(t, buf.String(), `"query":""`)
}
