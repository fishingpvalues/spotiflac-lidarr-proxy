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

// TestProcessDownloadFailsJobWhenWallClockBudgetExpires guards against a bug
// where the fallback loop returned early when the job's wall-clock deadline
// fired, skipping failJob: the row stayed in Downloading forever, Lidarr saw
// a pending download that never moved, and only a container restart
// (RecoverStuckJobs) ever cleared it. Observed in production 2026-08-21: a
// job sat "Downloading" at 0 B/s for hours after its budget expired. The job
// must instead be marked Failed and moved to history.
func TestProcessDownloadFailsJobWhenWallClockBudgetExpires(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		OutputDir:        dir,
		MaxConcurrent:    1,
		JobTimeout:       5 * time.Second,
		FallbackServices: []string{"qobuz"},
	}
	st := storage.New(dir)
	q, err := queue.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { q.Close() })

	scriptPath := filepath.Join(t.TempDir(), "spotiflac-cli")
	script := `#!/bin/bash
echo '{"type":"error","message":"unavailable"}'
exit 1
`
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0755))
	client := apispotiflac.NewClient(scriptPath, 5*time.Second, "tidal", "lossless", "", "", "", nil, "", nil)
	handler := sabnzbd.NewHandler(q, client, st, cfg, "0.1.0-test")

	job := &queue.Job{NzoID: "SABnzbd_nzo_budget001", Service: "tidal", SpotifyURL: "https://open.spotify.com/album/budget"}
	require.NoError(t, q.Add(job))
	// Rewind TimeAdded past TimeAdded+2*JobTimeout so the parent context is
	// already expired when processDownload runs: the primary attempt aborts
	// immediately and the fallback loop hits the expired-context branch.
	job.TimeAdded = time.Now().Add(-11 * time.Second)

	handler.ProcessDownloadSync(job)

	_, err = q.Get("SABnzbd_nzo_budget001")
	require.Error(t, err, "job must have left the active queue instead of sitting in Downloading")

	hist, _, err := q.History(queue.ListParams{Limit: 10})
	require.NoError(t, err)
	require.Len(t, hist, 1)
	assert.Equal(t, sabtypes.StatusFailed, hist[0].Status)
	assert.Contains(t, hist[0].ErrorMessage, "wall-clock budget")
}
