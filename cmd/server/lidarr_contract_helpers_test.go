package main

import (
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/rs/zerolog"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/config"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/queue"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/storage"
	sabtypes "github.com/fishingpvalues/spotiflac-lidarr-proxy/pkg/sabnzbd"
)

// testAppWithJob is testApp plus one job in the queue and in history, so the
// download-client contract can be asserted on real output instead of on an
// empty list.
func testAppWithJob(t *testing.T) (*fiber.App, *config.Config) {
	t.Helper()
	dir := t.TempDir()

	q, err := queue.New(filepath.Join(dir, "queue.db"))
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	t.Cleanup(func() { _ = q.Close() })

	cfg := &config.Config{
		APIKey:             testAPIKey,
		Port:               8484,
		OutputDir:          dir,
		DBPath:             filepath.Join(dir, "queue.db"),
		SpotiflacCLIPath:   filepath.Join(dir, "spotiflac-cli"),
		DefaultService:     "tidal",
		DefaultQuality:     "lossless",
		JobTimeout:         time.Minute,
		MetricsRequireAuth: true,
	}

	// One queued job, and one completed job whose history slot Lidarr has to
	// be able to import.
	queued := &queue.Job{
		NzoID:      "SABnzbd_nzo_contract_queue",
		SpotifyURL: "https://open.spotify.com/album/contract",
		Service:    "tidal",
		Category:   "music",
		Filename:   "Contract Test Artist - Contract Test Album [FLAC]",
		Status:     sabtypes.StatusQueued,
		Size:       36 * 1024 * 1024,
		Sizeleft:   18 * 1024 * 1024,
		Percentage: 50,
	}
	if err := q.Add(queued); err != nil {
		t.Fatalf("add queued job: %v", err)
	}

	done := &queue.Job{
		NzoID:      "SABnzbd_nzo_contract_done",
		SpotifyURL: "https://open.spotify.com/album/contract-done",
		Service:    "tidal",
		Category:   "music",
		Filename:   "Contract Test Artist - Contract Test Album [FLAC]",
		Status:     sabtypes.StatusCompleted,
		Size:       36 * 1024 * 1024,
		OutputPath: filepath.Join(dir, "SABnzbd_nzo_contract_done"),
	}
	if err := q.Add(done); err != nil {
		t.Fatalf("add completed job: %v", err)
	}
	if err := q.MoveToHistory(done.NzoID); err != nil {
		t.Fatalf("move to history: %v", err)
	}

	client := spotiflac.NewClient(cfg.SpotiflacCLIPath, cfg.JobTimeout,
		cfg.DefaultService, cfg.DefaultQuality, "", "", "", nil, "", nil)

	return buildApp(cfg, q, client, storage.New(cfg.OutputDir), zerolog.Nop(), "test"), cfg
}

// readAll drains a response body as a string.
func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}
