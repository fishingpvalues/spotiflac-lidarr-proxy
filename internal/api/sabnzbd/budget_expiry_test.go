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

// TestProcessDownloadGivesStaleQueuedJobFreshBudget guards the 2026-08-22
// incident: the wall-clock budget used to be measured from TimeAdded, so a
// job that sat queued through an outage arrived at its slot already out of
// time and failed instantly - a ~30-job backlog drained into mass "budget
// exhausted" failures minutes after the API recovered. The budget must bound
// ACTIVE processing time instead: a stale job gets a fresh budget and fails
// (or succeeds) on real service results, not on queue wait.
func TestProcessDownloadGivesStaleQueuedJobFreshBudget(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		OutputDir:        dir,
		MaxConcurrent:    1,
		JobTimeout:       30 * time.Second, // budget 60s >> backoff total 20s
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

	job := &queue.Job{NzoID: "SABnzbd_nzo_stale001", Service: "tidal", SpotifyURL: "https://open.spotify.com/album/stale"}
	require.NoError(t, q.Add(job))
	// Two hours in the queue: under the old TimeAdded-based budget (2x30s)
	// this job would have died before its first attempt.
	job.TimeAdded = time.Now().Add(-2 * time.Hour)

	handler.ProcessDownloadSync(job)

	_, err = q.Get("SABnzbd_nzo_stale001")
	require.Error(t, err, "job must have left the active queue")

	hist, _, err := q.History(queue.ListParams{Limit: 10})
	require.NoError(t, err)
	require.Len(t, hist, 1)
	assert.Equal(t, sabtypes.StatusFailed, hist[0].Status)
	assert.Contains(t, hist[0].ErrorMessage, "unavailable",
		"failure must come from the backend's own error, not the queue wait")
	assert.NotContains(t, hist[0].ErrorMessage, "wall-clock budget",
		"a stale-but-freshly-processed job must not burn its budget on queue wait")
}

// TestProcessDownloadFailsJobWhenProcessingBudgetExpires guards against a bug
// where the fallback loop returned early when the job's wall-clock deadline
// fired, skipping failJob: the row stayed in Downloading forever, Lidarr saw
// a pending download that never moved, and only a container restart
// (RecoverStuckJobs) ever cleared it. Observed in production 2026-08-21: a
// job sat "Downloading" at 0 B/s for hours after its budget expired. A job
// whose ACTIVE processing outruns the budget must be marked Failed and moved
// to history.
func TestProcessDownloadFailsJobWhenProcessingBudgetExpires(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		OutputDir:        dir,
		MaxConcurrent:    1,
		JobTimeout:       5 * time.Second, // budget 10s < attempt(8s)+backoff(5s)
		FallbackServices: []string{"qobuz"},
	}
	st := storage.New(dir)
	q, err := queue.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { q.Close() })

	scriptPath := filepath.Join(t.TempDir(), "spotiflac-cli")
	script := `#!/bin/bash
sleep 8
echo '{"type":"error","message":"slow backend"}'
exit 1
`
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0755))
	client := apispotiflac.NewClient(scriptPath, 10*time.Second, "tidal", "lossless", "", "", "", nil, "", nil)
	handler := sabnzbd.NewHandler(q, client, st, cfg, "0.1.0-test")

	job := &queue.Job{NzoID: "SABnzbd_nzo_budget001", Service: "tidal", SpotifyURL: "https://open.spotify.com/album/budget"}
	require.NoError(t, q.Add(job))

	handler.ProcessDownloadSync(job)

	_, err = q.Get("SABnzbd_nzo_budget001")
	require.Error(t, err, "job must have left the active queue instead of sitting in Downloading")

	hist, _, err := q.History(queue.ListParams{Limit: 10})
	require.NoError(t, err)
	require.Len(t, hist, 1)
	assert.Equal(t, sabtypes.StatusFailed, hist[0].Status)
	assert.Contains(t, hist[0].ErrorMessage, "wall-clock budget")
}
