package sabnzbd

import (
	"time"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/queue"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
)

// SetRetryBackoffForTest replaces the retry backoff schedule and returns a
// function restoring the previous one.
//
// The real schedule sleeps 5s then 15s between attempts. Multiplied by the
// retry count and the service fallback chain, four tests spent about 102
// seconds each doing nothing but sleeping, and the package as a whole took
// 605s - past `go test`'s default 600s per-package timeout, so a slightly
// slow runner failed the build for no reason at all.
//
// Only the durations are shortened; the number of attempts and the order of
// the fallback chain are what the tests actually assert on.
func SetRetryBackoffForTest(schedule []time.Duration) func() {
	previous := retryBackoff
	retryBackoff = schedule
	return func() { retryBackoff = previous }
}

// HandleProgressEventForTest applies one non-terminal CLI event to a job, the
// way a live download does. Exported for the progress tests, which live in
// the external test package.
func (h *Handler) HandleProgressEventForTest(job *queue.Job, evt spotiflac.ProgressEvent) {
	h.handleProgressEvent(job, evt)
}

// HandleCompleteEventForTest finalizes a job from a terminal CLI event.
func (h *Handler) HandleCompleteEventForTest(job *queue.Job, evt spotiflac.ProgressEvent) (bool, string) {
	return h.handleCompleteEvent(job, evt)
}
