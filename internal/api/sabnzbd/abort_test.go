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
)

// TestFailedAttemptKillsLingeringBackend guards against a backend process
// outliving its terminal error event. The handler returns on the first error
// line while the CLI keeps running (waiting on a verification callback or
// finishing remaining tracks); the next attempt then starts in the SAME job
// dir with the previous process still alive - shared bolt state files turn
// that overlap into "ISRC cache: timeout" warnings (observed live 2026-08-21)
// and both processes can write audio files into one directory. A failed
// attempt must kill its backend before the next one starts.
func TestFailedAttemptKillsLingeringBackend(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{OutputDir: dir, MaxConcurrent: 1, JobTimeout: 30 * time.Second}
	st := storage.New(dir)
	q, err := queue.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { q.Close() })

	// Fails fast with an error event, then lingers like a real CLI waiting on
	// a verification callback. Without the kill, this sleep would keep the
	// process (and its bolt locks) alive long past the next attempt.
	scriptPath := filepath.Join(t.TempDir(), "spotiflac-cli")
	script := `#!/bin/bash
echo '{"type":"error","message":"unavailable"}'
sleep 120
`
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0755))
	client := apispotiflac.NewClient(scriptPath, 5*time.Second, "tidal", "lossless", "", "", "", nil, "", nil)
	handler := sabnzbd.NewHandler(q, client, st, cfg, "0.1.0-test")

	job := &queue.Job{NzoID: "SABnzbd_nzo_abort001", Service: "tidal", SpotifyURL: "https://open.spotify.com/album/abort"}
	require.NoError(t, q.Add(job))

	start := time.Now()
	handler.ProcessDownloadSync(job)
	elapsed := time.Since(start)

	assert.Less(t, elapsed, 20*time.Second,
		"the job must not wait out the lingering backend's 120s sleep")
	jobDir := filepath.Join(dir, "SABnzbd_nzo_abort001")
	assert.True(t, client.AbortedActiveForTest(jobDir),
		"a failed attempt must kill its lingering backend before the next one starts")
}
