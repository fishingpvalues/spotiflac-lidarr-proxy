package newznab_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/api"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/api/newznab"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
)

func setupNewznabApp(t *testing.T) *fiber.App {
	t.Helper()

	client := spotiflac.NewClient("echo", 5*time.Second, "tidal", "lossless")
	handler := newznab.NewHandler(client, "http://localhost:8484")

	app := fiber.New()
	app.Use(api.APIKeyAuthWithSkiplist("test-key", "caps"))
	handler.RegisterRoutes(app)

	return app
}

func TestCaps(t *testing.T) {
	app := setupNewznabApp(t)

	req, _ := http.NewRequest("GET", "/api/newznab?t=caps&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "xml")
}

func TestCapsNoAuth(t *testing.T) {
	app := setupNewznabApp(t)

	req, _ := http.NewRequest("GET", "/api/newznab?t=caps", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "xml")
}

func TestSearch(t *testing.T) {
	app := setupNewznabApp(t)

	req, _ := http.NewRequest("GET", "/api/newznab?t=search&q=Test+Artist+Test+Album&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
}

func TestMusic(t *testing.T) {
	app := setupNewznabApp(t)

	req, _ := http.NewRequest("GET", "/api/newznab?t=music&artist=Test+Artist&album=Test+Album&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
}
