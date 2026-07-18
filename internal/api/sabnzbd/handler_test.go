package sabnzbd_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/api"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/api/sabnzbd"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/config"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/queue"
	apispotiflac "github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/storage"
	sabtypes "github.com/fishingpvalues/spotiflac-lidarr-proxy/pkg/sabnzbd"
)

func setupTestApp(t *testing.T) (*fiber.App, *queue.SQLiteQueue) {
	t.Helper()

	cfg := &config.Config{
		APIKey:         "test-key",
		OutputDir:      t.TempDir(),
		DefaultService: "tidal",
		DefaultQuality: "lossless",
		MaxConcurrent:  1,
		JobTimeout:     30 * time.Minute,
	}

	q, err := queue.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { q.Close() })

	st := storage.New(cfg.OutputDir)

	client := apispotiflac.NewClient("echo", 5*time.Second, "tidal", "lossless")

	handler := sabnzbd.NewHandler(q, client, st, cfg, "0.1.0-test")

	app := fiber.New()
	app.Use(api.APIKeyAuthWithSkiplist("test-key", "version", "auth"))
	handler.RegisterRoutes(app)

	return app, q
}

func TestVersion(t *testing.T) {
	app, _ := setupTestApp(t)

	req, _ := http.NewRequest("GET", "/api/sabnzbd?mode=version&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	var v sabtypes.VersionResponse
	json.NewDecoder(resp.Body).Decode(&v)
	assert.Equal(t, "0.1.0-test", v.Version)
}

func TestAuth(t *testing.T) {
	app, _ := setupTestApp(t)

	req, _ := http.NewRequest("GET", "/api/sabnzbd?mode=auth&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	var a sabtypes.AuthResponse
	json.NewDecoder(resp.Body).Decode(&a)
	assert.True(t, a.Auth)
}

func TestGetCats(t *testing.T) {
	app, _ := setupTestApp(t)

	req, _ := http.NewRequest("GET", "/api/sabnzbd?mode=get_cats&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	var c sabtypes.CategoriesResponse
	json.NewDecoder(resp.Body).Decode(&c)
	assert.Len(t, c.Categories, 17)
}

func TestAddURL(t *testing.T) {
	app, _ := setupTestApp(t)

	req, _ := http.NewRequest("POST", "/api/sabnzbd?mode=addurl&name=https://open.spotify.com/album/test123&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	var r sabtypes.AddURLResponse
	json.NewDecoder(resp.Body).Decode(&r)
	assert.True(t, r.Status)
	assert.Len(t, r.NzoIDs, 1)
	assert.Contains(t, r.NzoIDs[0], "SABnzbd_nzo_")
}

func TestQueue(t *testing.T) {
	app, _ := setupTestApp(t)

	req, _ := http.NewRequest("GET", "/api/sabnzbd?mode=queue&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	var q sabtypes.QueueResponse
	json.NewDecoder(resp.Body).Decode(&q)
	assert.Equal(t, "0.1.0-test", q.Queue.Version)
}

func TestHistory(t *testing.T) {
	app, _ := setupTestApp(t)

	req, _ := http.NewRequest("GET", "/api/sabnzbd?mode=history&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	var h sabtypes.HistoryResponse
	json.NewDecoder(resp.Body).Decode(&h)
	assert.Equal(t, "0.1.0-test", h.History.Version)
}

func TestAuthRejected(t *testing.T) {
	app, _ := setupTestApp(t)

	// version endpoint should work without API key
	req, _ := http.NewRequest("GET", "/api/sabnzbd?mode=version", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	// queue endpoint should reject without API key
	req2, _ := http.NewRequest("GET", "/api/sabnzbd?mode=queue", nil)
	resp2, err := app.Test(req2)
	require.NoError(t, err)
	assert.Equal(t, 401, resp2.StatusCode)
}

func TestChangeCatUpdatesServiceAndQuality(t *testing.T) {
	app, q := setupTestApp(t)

	job := &queue.Job{
		NzoID:      "SABnzbd_nzo_changecat",
		SpotifyURL: "https://open.spotify.com/album/test",
		Category:   "music-flac-16",
		Service:    "tidal",
		Quality:    "lossless",
	}
	require.NoError(t, q.Add(job))

	req, _ := http.NewRequest("GET",
		"/api/sabnzbd?mode=change_cat&value=SABnzbd_nzo_changecat&value2=music-qobuz-flac-24&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	got, err := q.Get("SABnzbd_nzo_changecat")
	require.NoError(t, err)
	assert.Equal(t, "music-qobuz-flac-24", got.Category)
	assert.Equal(t, "qobuz", got.Service)
	assert.Equal(t, "hires", got.Quality)
}
