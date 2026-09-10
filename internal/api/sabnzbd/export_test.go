package sabnzbd

import (
	"time"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/breaker"
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

// SetMaxCircuitParkForTest shortens the cap on how long a job may sit parked
// waiting for an open circuit to close, and returns a restore func.
func SetMaxCircuitParkForTest(d time.Duration) func() {
	previous := maxCircuitPark
	maxCircuitPark = d
	return func() { maxCircuitPark = previous }
}

// SetCircuitParkPollForTest shortens the park loop's poll interval.
func SetCircuitParkPollForTest(d time.Duration) func() {
	previous := circuitParkPoll
	circuitParkPoll = d
	return func() { circuitParkPoll = previous }
}

// SetBreakerForTest replaces the handler's circuit breaker so a test can use
// a cooldown measured in milliseconds instead of the production 10 minutes.
func (h *Handler) SetBreakerForTest(threshold int, cooldown time.Duration) {
	h.breaker = breaker.New(threshold, cooldown)
}

// BreakerOpenForTest reports whether the named service's circuit is currently
// open. Tests used to observe this indirectly, by running one more job and
// asserting its failure message said "circuit open" - which stopped working
// once an open circuit parks the job instead of failing it. Asking the
// breaker directly tests the same invariant and does not depend on what the
// handler decides to do about it.
func (h *Handler) BreakerOpenForTest(service string) bool {
	return h.breaker.Status()[service].Open
}
