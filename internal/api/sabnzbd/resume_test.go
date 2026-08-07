package sabnzbd

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/config"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/queue"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/storage"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/pkg/sabnzbd"
)

// resumeHandler builds a Handler over a throwaway queue whose downloads
// always fail fast, so ResumeQueuedJobs can be observed draining the queue
// without shelling out to anything real.
func resumeHandler(t *testing.T) (*Handler, *queue.SQLiteQueue) {
	t.Helper()

	dir := t.TempDir()
	q, err := queue.New(filepath.Join(dir, "queue.db"))
	if err != nil {
		t.Fatalf("queue.New: %v", err)
	}
	t.Cleanup(func() { _ = q.Close() })

	cfg := &config.Config{
		OutputDir:      dir,
		DefaultService: "tidal",
		DefaultQuality: "lossless",
		MaxConcurrent:  2,
		JobTimeout:     5 * time.Second,
	}
	// A CLI path that does not exist: every attempt fails immediately, which
	// is all this test needs - it asserts the jobs get picked up at all.
	client := spotiflac.NewClient(filepath.Join(dir, "no-such-cli"), cfg.JobTimeout,
		cfg.DefaultService, cfg.DefaultQuality, "", "", "", nil, filepath.Join(dir, "no-python"), nil)

	h := NewHandler(q, client, storage.New(cfg.OutputDir), cfg, "test")
	return h, q
}

func TestResumeQueuedJobsPicksUpJobsStrandedByRestart(t *testing.T) {
	h, q := resumeHandler(t)

	// Jobs persisted as Queued by a process that then died. Production had
	// 13 of these sitting untouched for four days across several restarts.
	for _, id := range []string{"SABnzbd_nzo_aaa", "SABnzbd_nzo_bbb", "SABnzbd_nzo_ccc"} {
		if err := q.Add(&queue.Job{
			NzoID:      id,
			SpotifyURL: "https://open.spotify.com/track/" + id,
			Category:   "music-flac-16",
			Service:    "tidal",
			Quality:    "lossless",
		}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}

	if got := h.ResumeQueuedJobs(); got != 3 {
		t.Fatalf("ResumeQueuedJobs() = %d, want 3", got)
	}

	// Each resumed job must leave Queued. Anything still Queued once the
	// dispatchers have run is the stranding bug reappearing.
	deadline := time.Now().Add(30 * time.Second)
	for {
		jobs, _, err := q.List(queue.ListParams{Status: string(sabnzbd.StatusQueued)})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(jobs) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d job(s) still Queued after resume", len(jobs))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestResumeQueuedJobsIsANoOpOnAnEmptyQueue(t *testing.T) {
	h, _ := resumeHandler(t)
	if got := h.ResumeQueuedJobs(); got != 0 {
		t.Fatalf("ResumeQueuedJobs() = %d, want 0", got)
	}
}
