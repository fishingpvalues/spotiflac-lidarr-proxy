package sabnzbd_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/api/sabnzbd"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/config"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/queue"
	apispotiflac "github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/storage"
	sabtypes "github.com/fishingpvalues/spotiflac-lidarr-proxy/pkg/sabnzbd"
)

// TestUpstreamBreakParksQueue verifies the end-to-end wiring: a job whose
// failure carries the community infra's "scheduled short break ... N
// minute(s)" announcement must park the queue, so a job added right after
// stays Queued (holding no concurrency slot) instead of walking the dead
// infra. Observed 2026-08-22 without this: a 47-job burst triggered a
// 104-minute break and every queued job still burned ~90 s of requests
// against the dead API before failing into Lidarr history.
func TestUpstreamBreakParksQueue(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		OutputDir:        dir,
		MaxConcurrent:    1,
		JobTimeout:       30 * time.Second,
		FallbackServices: []string{"qobuz"},
	}
	st := storage.New(dir)
	q, err := queue.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { q.Close() })

	scriptPath := filepath.Join(t.TempDir(), "spotiflac-cli")
	script := `#!/bin/bash
echo '{"type":"error","message":"track X: Download failed: The server is taking a scheduled short break. Please try again in about 2 minute(s)."}'
exit 1
`
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0755))
	client := apispotiflac.NewClient(scriptPath, 5*time.Second, "tidal", "lossless", "", "", "", nil, "", nil)
	handler := sabnzbd.NewHandler(q, client, st, cfg, "0.1.0-test")

	// Job 1 hits the break announcement and fails; its final error carries
	// the message, which arms the gate for ~2 minutes.
	job1 := &queue.Job{NzoID: "SABnzbd_nzo_break001", Service: "tidal", SpotifyURL: "https://open.spotify.com/album/break1"}
	require.NoError(t, q.Add(job1))
	handler.ProcessDownloadSync(job1)

	hist, _, err := q.History(queue.ListParams{Limit: 10})
	require.NoError(t, err)
	require.Len(t, hist, 1)
	assert.Equal(t, sabtypes.StatusFailed, hist[0].Status)
	assert.Contains(t, hist[0].ErrorMessage, "short break")

	// Job 2 arrives while the break window is active: it must stay Queued
	// (parked at the gate, holding no slot) rather than start downloading.
	job2 := &queue.Job{NzoID: "SABnzbd_nzo_break002", Service: "tidal", SpotifyURL: "https://open.spotify.com/album/break2"}
	require.NoError(t, q.Add(job2))
	go handler.ProcessDownloadSync(job2)

	time.Sleep(3 * time.Second)
	got, err := q.Get("SABnzbd_nzo_break002")
	require.NoError(t, err)
	assert.Equal(t, sabtypes.StatusQueued, got.Status,
		"job parked at the upstream-break gate must remain Queued, not start")
}
