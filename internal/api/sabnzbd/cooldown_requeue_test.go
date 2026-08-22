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

// TestRateLimitRequeuesInsteadOfFailing guards the 2026-08-22 drain incident.
// The community API answers a burst with 429s, and a 429 says nothing about
// the release - only about our request rate. Failing the job on it is a lie
// Lidarr cannot recover from on its own: the item lands in history and only an
// external re-grab cycle ever brings it back. Measured that day: one job held
// the single concurrency slot for ~25 minutes retrying into 429s while six
// more queued behind it and the backlog flatlined at 173 missing albums.
//
// So a 429 must park the queue and put the job BACK, still Queued.
func TestRateLimitRequeuesInsteadOfFailing(t *testing.T) {
	// Long park: the requeued job's own goroutine then sits in
	// parkForUpstreamBreak for the whole test instead of racing it.
	t.Setenv("SPF_RATE_LIMIT_PARK_S", "3600")

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
echo '{"type":"error","message":"track X: Download failed: all requested Tidal qualities failed: failed to get download URL: Tidal community API rate limited (429)"}'
exit 1
`
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0755))
	client := apispotiflac.NewClient(scriptPath, 5*time.Second, "tidal", "lossless", "", "", "", nil, "", nil)
	handler := sabnzbd.NewHandler(q, client, st, cfg, "0.1.0-test")

	job := &queue.Job{NzoID: "SABnzbd_nzo_ratelimit1", Service: "tidal", SpotifyURL: "https://open.spotify.com/album/rl"}
	require.NoError(t, q.Add(job))

	handler.ProcessDownloadSync(job)

	// Still in the ACTIVE queue, not in history.
	got, err := q.Get("SABnzbd_nzo_ratelimit1")
	require.NoError(t, err, "a rate-limited job must stay in the queue, not move to history")
	assert.Equal(t, sabtypes.StatusQueued, got.Status)
	assert.Empty(t, got.ErrorMessage, "a requeued job must not carry a failure message")

	hist, _, err := q.History(queue.ListParams{Limit: 10})
	require.NoError(t, err)
	assert.Empty(t, hist, "nothing may reach history on a pure rate-limit failure")
}

// TestReleaseFailureStillFails is the other half of the contract: a failure
// that is genuinely about the release must NOT be requeued, or a permanently
// broken album becomes a download that is forever about to start.
func TestReleaseFailureStillFails(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{OutputDir: dir, MaxConcurrent: 1, JobTimeout: 30 * time.Second}
	st := storage.New(dir)
	q, err := queue.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { q.Close() })

	scriptPath := filepath.Join(t.TempDir(), "spotiflac-cli")
	script := `#!/bin/bash
echo '{"type":"error","message":"no matching release found"}'
exit 1
`
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0755))
	client := apispotiflac.NewClient(scriptPath, 5*time.Second, "tidal", "lossless", "", "", "", nil, "", nil)
	handler := sabnzbd.NewHandler(q, client, st, cfg, "0.1.0-test")

	job := &queue.Job{NzoID: "SABnzbd_nzo_realfail1", Service: "tidal", SpotifyURL: "https://open.spotify.com/album/rf"}
	require.NoError(t, q.Add(job))

	handler.ProcessDownloadSync(job)

	_, err = q.Get("SABnzbd_nzo_realfail1")
	require.Error(t, err, "a genuine release failure must leave the active queue")
	hist, _, err := q.History(queue.ListParams{Limit: 10})
	require.NoError(t, err)
	require.Len(t, hist, 1)
	assert.Equal(t, sabtypes.StatusFailed, hist[0].Status)
}
