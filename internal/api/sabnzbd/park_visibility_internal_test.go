package sabnzbd

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/config"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/queue"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/storage"
)

// The park has to be VISIBLE. From outside the process it is
// indistinguishable from a hang - slots in the queue, 0 B/s, and
// deliberately no outbound requests - and the external stuck-monitor on
// potatostack watched exactly those two numbers and restarted this container
// eight times in the week to 2026-10-02, each time throwing away the
// breakers, the park and every in-flight job.
func TestCircuitParkPublishesItsState(t *testing.T) {
	h := noPythonHandler(t)
	h.breaker.RecordFailure(config.ServiceTidal)
	h.breaker.RecordFailure(config.ServiceQobuz)
	h.breaker.RecordFailure(config.ServiceAmazon)
	// The threshold is 5, so drive each to it.
	for i := 0; i < 4; i++ {
		h.breaker.RecordFailure(config.ServiceTidal)
		h.breaker.RecordFailure(config.ServiceQobuz)
		h.breaker.RecordFailure(config.ServiceAmazon)
	}

	prevPoll := circuitParkPoll
	circuitParkPoll = 10 * time.Millisecond
	defer func() { circuitParkPoll = prevPoll }()

	job := &queue.Job{NzoID: "SABnzbd_nzo_parkvis", Service: config.ServiceTidal}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.parkForOpenCircuits(ctx, job)
	}()

	// While parked the state must be published...
	deadline := time.Now().Add(2 * time.Second)
	for {
		if remaining, services := h.circuitParkState(); remaining > 0 {
			assert.Contains(t, services, config.ServiceTidal)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a parked job must publish the circuit park, or nothing outside can tell it from a hang")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// ...and cleared once the park is over.
	cancel()
	<-done
	remaining, _ := h.circuitParkState()
	assert.Zero(t, remaining, "the park must not stay published after it ends")
}

// A job that never ran must not be failed for a circuit that was open while
// it waited for its slot. The park runs BEFORE the semaphore, but the wait
// for the semaphore is unbounded, so the breakers can be reopened by the jobs
// that ran ahead - measured 2026-10-02, job SABnzbd_nzo_33ed151d-eb2 parked
// at 12:59:41 and failed at 14:03:22 with "circuit open" and no attempt in
// between.
func TestNeverAttemptedJobIsRequeuedNotFailed(t *testing.T) {
	h := noPythonHandlerWithQueue(t)
	job := &queue.Job{
		NzoID:      "SABnzbd_nzo_neverran",
		SpotifyURL: "https://open.spotify.com/album/neverran",
		Service:    config.ServiceTidal,
		Filename:   "Never Attempted",
	}
	require.NoError(t, h.queue.Add(job))

	out := &jobOutcome{} // attempted stays false: nothing ever ran
	h.concludeFailedAttempts(context.Background(), job,
		"service tidal temporarily unavailable (circuit open)", out)

	back, err := h.queue.Get(job.NzoID)
	require.NoError(t, err, "a job that was never attempted must go back to the queue, not into Lidarr's history")
	assert.Equal(t, "Queued", string(back.Status))

	hist, _, err := h.queue.History(queue.ListParams{Limit: 5})
	require.NoError(t, err)
	assert.Empty(t, hist)
}

// A job whose every candidate circuit is open is never reported as failed
// with "circuit open" - that string was 151 of 161 retained failures and not
// one of them was a download that had been attempted and lost.
func TestNeverAttemptedRequeueIsBounded(t *testing.T) {
	h := noPythonHandlerWithQueue(t)
	job := &queue.Job{
		NzoID:      "SABnzbd_nzo_bounded",
		SpotifyURL: "https://open.spotify.com/album/bounded",
		Service:    config.ServiceTidal,
		Filename:   "Bounded",
	}
	require.NoError(t, h.queue.Add(job))

	for i := 0; i <= maxUnattemptedRequeues; i++ {
		out := &jobOutcome{}
		h.concludeFailedAttempts(context.Background(), job,
			"service tidal temporarily unavailable (circuit open)", out)
	}

	// The bound exists so a permanently broken release cannot hide as an
	// eternally pending download; after it is spent the job is failed with
	// the reason it actually had.
	hist, _, err := h.queue.History(queue.ListParams{Limit: 5})
	require.NoError(t, err)
	require.Len(t, hist, 1, "the requeue budget must be finite")
	assert.Contains(t, hist[0].ErrorMessage, "circuit open")
}

// The stuck-job warning must measure the RUN. TimeAdded is when the job
// entered the queue, which with a backlog is hours before it ever took a
// slot, so every queued job was reported as "downloading for longer than 2x
// the timeout" the moment the check looked at it.
func TestStuckJudgementUsesProcessingStart(t *testing.T) {
	h := noPythonHandlerWithQueue(t)

	old := time.Now().Add(-10 * time.Hour) // queued long ago
	started := time.Now().Add(-time.Second)
	job := &queue.Job{
		NzoID:      "SABnzbd_nzo_longwait",
		SpotifyURL: "https://open.spotify.com/album/longwait",
		Service:    config.ServiceTidal,
		Status:     "Downloading",
		Filename:   "Long Wait",
	}
	require.NoError(t, h.queue.Add(job))
	job.TimeAdded = old
	job.StartedAt = &started
	job.Status = "Downloading"
	require.NoError(t, h.queue.Update(job))

	// It has been QUEUED for ten hours but RUNNING for one second: not stuck.
	_, stuck := h.stuckJobsForTest()
	assert.Empty(t, stuck, "a job that just started must not be reported as stuck because it waited in the queue")

	far := time.Now().Add(-4 * time.Hour)
	job.StartedAt = &far
	require.NoError(t, h.queue.Update(job))
	_, stuck = h.stuckJobsForTest()
	assert.Len(t, stuck, 1, "a job that has genuinely been running past its budget must be reported")
}

// The park state has to reach mode=warnings too: that is what the external
// monitor polls, and /health alone was not enough to stop it restarting the
// container.
func TestWarningsCarryTheCircuitParkID(t *testing.T) {
	h := noPythonHandlerWithQueue(t)
	h.beginCircuitPark([]string{config.ServiceTidal}, 5*time.Minute)
	defer h.endCircuitPark()

	ids, err := h.warningIDs()
	require.NoError(t, err)
	assert.Contains(t, ids, "circuit_park")
}

func noPythonHandlerWithQueue(t *testing.T) *Handler {
	t.Helper()
	h := noPythonHandler(t)
	q, err := queue.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { q.Close() })
	out := t.TempDir()
	h.cfg.OutputDir = out
	h.cfg.JobTimeout = 30 * time.Minute
	h.queue = q
	h.storage = storage.New(out)
	return h
}
