package queue

import (
	"time"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/pkg/sabnzbd"
)

type Job struct {
	ID           int64             `json:"-"`
	NzoID        string            `json:"nzo_id"`
	SpotifyURL   string            `json:"spotify_url"`
	Status       sabnzbd.JobStatus `json:"status"`
	Category     string            `json:"category"`
	Priority     string            `json:"priority"`
	Filename     string            `json:"filename"`
	OutputPath   string            `json:"output_path"`
	Size         int64             `json:"size"`
	Sizeleft     int64             `json:"sizeleft"`
	Percentage   float64           `json:"percentage"`
	TimeAdded    time.Time         `json:"time_added"`
	CompletedAt  *time.Time        `json:"completed_at,omitempty"`
	ErrorMessage string            `json:"error_message,omitempty"`
	Service      string            `json:"service"`
	Quality      string            `json:"quality"`
	TrackCount   int               `json:"track_count"`
	CLIOutput    string            `json:"-"`

	// StartedAt is when this job last took the concurrency slot, which is
	// NOT TimeAdded: with a backlog a job can sit Queued for hours, and the
	// "downloading for longer than the budget" check in mode=warnings read
	// TimeAdded, so a job that had only just started was reported as stuck.
	// Measured 2026-09-27: ~50 queued jobs, all of them past 2x the timeout
	// before any of them had run.
	StartedAt *time.Time `json:"started_at,omitempty"`
	// ProgressAt is the last time the backend reported actual movement
	// (bytes written or a completed track). It is the honest answer to "is
	// this download wedged?", which the external stuck-monitor needs and
	// which queue speed alone cannot give it: one backend invocation is
	// silent for up to SPF_JOB_TIMEOUT while it resolves and downloads, so
	// 0 B/s is the NORMAL reading for a healthy job.
	ProgressAt *time.Time `json:"progress_at,omitempty"`
	// RecoverCount is how many times an unclean restart has put this job
	// back in the queue. Bounded so a crash loop cannot re-download one
	// release forever.
	RecoverCount int `json:"recover_count"`
}

// MaxRecoveries is how often one job may be re-queued after an unclean
// restart before it is failed for real. A restart interrupts a download but
// does not prove the release is broken, so failing it on the first restart
// threw away a grab that Lidarr then had to blocklist and re-search:
// measured over the week to 2026-10-02, the DAGU stuck-monitor restarted the
// container eight times and every in-flight job died "interrupted by
// restart" - three of them were releases that had previously downloaded
// fine. Re-downloading the same release forever is worse, hence the bound.
const MaxRecoveries = 3

type ListParams struct {
	Start    int
	Limit    int
	Search   string
	NzoIDs   []string
	Status   string
	Category string
}
