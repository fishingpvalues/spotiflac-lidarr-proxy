package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/rs/zerolog"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/config"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/queue"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/storage"
)

const testAPIKey = "s3cr3t-test-key-do-not-log"

// testApp builds the real wired app - the same buildApp runServe calls - over
// a throwaway queue and output dir.
func testApp(t *testing.T) (*fiber.App, *config.Config) {
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

	client := spotiflac.NewClient(cfg.SpotiflacCLIPath, cfg.JobTimeout,
		cfg.DefaultService, cfg.DefaultQuality, "", "", "", nil, "", nil)

	return buildApp(cfg, q, client, storage.New(cfg.OutputDir), zerolog.Nop(), "test"), cfg
}
