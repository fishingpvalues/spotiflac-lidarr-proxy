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

// BreakWindowForTest reports the remaining upstream break pause and whether
// one is armed. Tests assert on the gate itself rather than inferring it from
// a job's fate.
func (h *Handler) BreakWindowForTest() (time.Duration, bool) {
	r := h.breakGate.remaining()
	return r, r > 0
}

// ObserveSpeedForTest runs the queue's byte-rate sampling for one job.
func (h *Handler) ObserveSpeedForTest(job *queue.Job) float64 {
	rate, _ := h.observeSpeed(job)
	return rate
}

// BackdateSpeedSampleForTest plants the previous sample a rate is computed
// against, so a test does not have to sleep for real time to pass.
func (h *Handler) BackdateSpeedSampleForTest(nzoID string, sizeleft int64, age time.Duration) {
	h.speedMu.Lock()
	defer h.speedMu.Unlock()
	if h.speedSamples == nil {
		h.speedSamples = make(map[string]speedSample)
	}
	h.speedSamples[nzoID] = speedSample{sizeleft: sizeleft, at: time.Now().Add(-age)}
}

// CircuitParkForTest reports the published circuit-park state.
func (h *Handler) CircuitParkForTest() (time.Duration, []string) {
	return h.circuitParkState()
}

// stuckJobsForTest runs the mode=warnings stuck-job scan and returns the
// warnings it would publish, so a test can assert on the judgement itself.
func (h *Handler) stuckJobsForTest() ([]string, []string) {
	var ids, texts []string
	stuck, _, err := h.queue.List(queue.ListParams{Status: "Downloading", Limit: 1000})
	if err != nil {
		return nil, nil
	}
	for _, job := range stuck {
		since := job.TimeAdded
		if job.StartedAt != nil {
			since = *job.StartedAt
		}
		if age := time.Since(since); age > 2*h.cfg.JobTimeout {
			ids = append(ids, job.NzoID)
			texts = append(texts, job.Filename)
		}
	}
	return ids, texts
}

// warningIDs returns the ids of the warnings mode=warnings would publish.
func (h *Handler) warningIDs() ([]string, error) {
	var ids []string
	for _, w := range h.Warnings() {
		ids = append(ids, w.ID)
	}
	return ids, nil
}

// DispatchJobForTest runs the production dispatch path, so a test can prove
// that a second dispatch for a job already in flight is a no-op.
func (h *Handler) DispatchJobForTest(job *queue.Job) {
	h.dispatchJob(job)
}

// RequeueAfterCooldownForTest exposes the cooldown requeue path so a test can
// exhaust its budget without parking the queue for real hours.
func (h *Handler) RequeueAfterCooldownForTest(job *queue.Job, cooldown time.Duration, lastErr string) (bool, string) {
	return h.requeueAfterCooldown(job, cooldown, lastErr)
}

// MaxCooldownRequeuesForTest exposes the bound to the external test package.
func MaxCooldownRequeuesForTest() int { return maxCooldownRequeues }

// InFlightForTest reports how many job workers are currently running, so a
// test can wait for them instead of racing the temp-dir cleanup.
func (h *Handler) InFlightForTest() int {
	n := 0
	h.inFlight.Range(func(_, _ any) bool {
		n++
		return true
	})
	return n
}
