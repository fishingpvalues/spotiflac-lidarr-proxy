package newznab_test

import (
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/api"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/api/newznab"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/indexer"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
)

// noPython points the client at a Python that does not exist, so search and
// download exercise the CLI path deterministically. An empty venv path makes
// findPython fall back to whatever python3 the machine has, and that one has
// no SpotiFLAC module - so the test would spawn a real interpreter, wait for
// it to fail, and only then reach the mock CLI. Harmless in isolation, slow
// and timing-dependent under a full-suite run.
const noPython = "/nonexistent/python3"

func setupNewznabApp(t *testing.T) *fiber.App {
	t.Helper()

	client := spotiflac.NewClient("echo", 5*time.Second, "tidal", "lossless", "", "", "", nil, noPython, nil)
	handler := newznab.NewHandler(client, "test", "test-key", "lossless")

	app := fiber.New()
	app.Use(api.APIKeyAuth("test-key", nil, []string{"caps"}))
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

func TestGetReturnsWellFormedNZB(t *testing.T) {
	// Lidarr fetches this URL itself and requires a real NZB (root element
	// "nzb") before it will even contact the download client - confirmed
	// against a real production Lidarr this session.
	app := setupNewznabApp(t)

	id := "https://open.spotify.com/album/x"
	req, _ := http.NewRequest("GET", "/api/newznab?t=get&id="+id+"&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "nzb")
}

func TestMusicSearch(t *testing.T) {
	// Lidarr sends t=musicsearch (Newznab spec standard), not t=music.
	// Prior bug: only "music" was handled; "musicsearch" fell through to
	// default empty-results handler.
	app := setupNewznabApp(t)

	req, _ := http.NewRequest("GET", "/api/newznab?t=musicsearch&q=debussy&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
}

func TestMusicSearchFallbackToQ(t *testing.T) {
	// When only q= is provided (no artist/album), handleMusic must use q
	// as the search query. Prior bug: it only used artist+album.
	app := setupNewznabApp(t)

	req, _ := http.NewRequest("GET", "/api/newznab?t=musicsearch&q=debussy&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
}

func TestGetMissingIDReturnsBadRequest(t *testing.T) {
	app := setupNewznabApp(t)

	req, _ := http.NewRequest("GET", "/api/newznab?t=get&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 400, resp.StatusCode)
}

func TestHandleGetFoldsReleaseNameSizeAndTracksIntoTheNZB(t *testing.T) {
	// Lidarr fetches this endpoint and re-uploads the bytes to the download
	// client with a POST that carries no nzbname and no size. The NZB is
	// therefore the only channel for them, and without the name the queue
	// slot's filename is empty and Lidarr never tracks the download.
	app := setupNewznabApp(t)

	req, _ := http.NewRequest("GET",
		"/api/newznab?t=get&id=https%3A%2F%2Fopen.spotify.com%2Falbum%2Fx&name=Daft+Punk+-+Discovery+%5BFLAC%5D&size=513802240&tracks=14&apikey=test-key",
		nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	release, err := indexer.ParseNZBMeta(body)
	require.NoError(t, err)
	assert.Equal(t, "https://open.spotify.com/album/x", release.SpotifyURL)
	assert.Equal(t, "Daft Punk - Discovery [FLAC]", release.Name)
	assert.Equal(t, int64(513802240), release.Size)
	assert.Equal(t, 14, release.TrackCount)
}

func TestHandleGetWithoutANameFallsBackToTheID(t *testing.T) {
	app := setupNewznabApp(t)

	req, _ := http.NewRequest("GET",
		"/api/newznab?t=get&id=https%3A%2F%2Fopen.spotify.com%2Falbum%2Fy&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	release, err := indexer.ParseNZBMeta(body)
	require.NoError(t, err)
	assert.Equal(t, "https://open.spotify.com/album/y", release.Name)
	assert.Zero(t, release.Size)
	assert.Zero(t, release.TrackCount)
}
