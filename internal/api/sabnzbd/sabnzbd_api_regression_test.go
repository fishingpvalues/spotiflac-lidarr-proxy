package sabnzbd_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/queue"
	sabtypes "github.com/fishingpvalues/spotiflac-lidarr-proxy/pkg/sabnzbd"
)

// mode=retry exists to re-run a FAILED download, and a failed download lives
// in history. It used to read the job through queue.Get, which filters
// `is_history = 0` - so it could only ever answer 404 for the one kind of job
// it is for. documented in docs/API.md as "Retry failed".
func TestRetryPullsAJobBackOutOfHistory(t *testing.T) {
	app, q := setupTestApp(t)

	job := &queue.Job{
		NzoID:      "SABnzbd_nzo_retryme",
		SpotifyURL: "https://open.spotify.com/album/retryme",
		Service:    "tidal",
		Filename:   "Retry Me",
	}
	require.NoError(t, q.Add(job))
	require.NoError(t, q.MoveToHistory(job.NzoID))

	req, _ := http.NewRequest("GET", "/api/sabnzbd?mode=retry&value="+job.NzoID+"&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode, "a history item must be retryable")

	var r sabtypes.RetryResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&r))
	assert.True(t, r.Status)

	// And it must be back in the ACTIVE queue: leaving is_history set meant
	// the retried job was invisible to every queue listing.
	back, err := q.Get(job.NzoID)
	require.NoError(t, err, "a retried job must be back in the active queue")
	assert.Equal(t, sabtypes.StatusQueued, back.Status)
}

// A history delete naming an ACTIVE job used to delete the active row while
// its worker kept running: queue.Delete has no is_history predicate, so the
// orphan re-created the output directory and held the concurrency slot for
// its whole budget.
func TestHistoryDeleteDoesNotRemoveAnActiveJob(t *testing.T) {
	app, q := setupTestApp(t)

	job := &queue.Job{
		NzoID:      "SABnzbd_nzo_activemusic",
		SpotifyURL: "https://open.spotify.com/album/active",
		Service:    "tidal",
		Status:     sabtypes.StatusDownloading,
		Filename:   "Still Running",
	}
	require.NoError(t, q.Add(job))
	job.Status = sabtypes.StatusDownloading
	require.NoError(t, q.Update(job))

	req, _ := http.NewRequest("GET",
		"/api/sabnzbd?mode=history&name=delete&value="+job.NzoID+"&del_files=0&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)

	_, err = q.Get(job.NzoID)
	assert.NoError(t, err, "an ACTIVE job must survive a history delete aimed at its id")
}

// A job's priority is reported the way SABnzbd reports it: as the name of a
// SabnzbdPriority member. Lidarr enum-parses the field, so passing through the
// number it sent (-100 for its default) silently gave every queue item the
// wrong priority.
func TestQueueSlotPriorityIsAName(t *testing.T) {
	app, q := setupTestApp(t)

	job := &queue.Job{
		NzoID:      "SABnzbd_nzo_prio",
		SpotifyURL: "https://open.spotify.com/album/prio",
		Service:    "tidal",
		Priority:   "-100",
		Status:     sabtypes.StatusQueued,
		Filename:   "Priority",
	}
	require.NoError(t, q.Add(job))

	req, _ := http.NewRequest("GET", "/api/sabnzbd?mode=queue&apikey=test-key", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)

	var qr sabtypes.QueueResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&qr))
	require.Len(t, qr.Queue.Slots, 1)
	assert.Equal(t, "Default", qr.Queue.Slots[0].Priority,
		"Lidarr enum-parses this field; a bare number is not a SabnzbdPriority name")
}

// dispatchJob is the single entry point for starting a job, and it must not
// start two workers for one nzo_id: they would share the output directory and
// the row, and when the first finishes and moves the row to history the
// second one's next progress write restores status=Downloading on a
// historical row that RecoverStuckJobs never repairs.
//
// The check is the overlap itself, not a call count: several dispatches for
// one job must never put two backend processes on disk at the same time.
func TestDispatchJobIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	lock := filepath.Join(dir, "lock")
	overlap := filepath.Join(dir, "overlap")
	cli := filepath.Join(dir, "spotiflac-cli")
	script := "#!/bin/sh\n" +
		"[ -e " + lock + " ] && echo overlap >> " + overlap + "\n" +
		"touch " + lock + "\n" +
		"sleep 0.2\n" +
		"rm -f " + lock + "\n" +
		"exit 1\n"
	require.NoError(t, os.WriteFile(cli, []byte(script), 0o755))

	h, q := failureHandler(t, cli, nil)
	job := &queue.Job{
		NzoID:      "SABnzbd_nzo_once",
		SpotifyURL: "https://open.spotify.com/album/once",
		Service:    "tidal",
		Filename:   "Run Once",
	}
	require.NoError(t, q.Add(job))

	for i := 0; i < 3; i++ {
		h.DispatchJobForTest(job)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(overlap); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, err := os.Stat(overlap)
	assert.True(t, os.IsNotExist(err), "two workers ran for one nzo_id at the same time")
}
