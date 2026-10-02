package sabnzbd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/rs/zerolog"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/api/verify"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/breaker"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/config"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/health"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/metrics"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/queue"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/storage"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/pkg/sabnzbd"
)

const maxConcurrent = 3

type Handler struct {
	queue       *queue.SQLiteQueue
	client      *spotiflac.Client
	storage     *storage.Storage
	cfg         *config.Config
	version     string
	log         zerolog.Logger
	sem         chan struct{}
	breaker     *breaker.Breaker
	breakGate   *upstreamBreakGate
	verifyStore *verify.Store
	// backendWarnings reports configuration under which no download can
	// complete; see SetBackendWarnings.
	backendWarnings func() []health.BackendWarning

	// requeues counts, per nzo_id, how many times a job has been put back
	// into the queue because upstream asked us to back off (scheduled break
	// or 429) rather than because the release itself failed. In-memory and
	// process-lifetime only: a restart resets it, and ResumeQueuedJobs
	// re-dispatches whatever is still Queued, so the worst case after a
	// restart is one more round of requeues.
	requeueMu sync.Mutex
	requeues  map[string]int

	// running maps an nzo_id to the cancel func of its in-flight download.
	// Deleting a job used to remove the row and leave the goroutine running,
	// so it kept its concurrency slot - with SPF_MAX_CONCURRENT=1 a single
	// abandoned job wedged the whole queue for as long as its retries and
	// service fallbacks took, which is hours. Observed: a freshly added job
	// sat "Queued" for 270s and never started.
	running sync.Map

	// parkMu guards parkUntil/parkServices, the operator-visible half of
	// parkForOpenCircuits (see beginCircuitPark).
	parkMu       sync.Mutex
	parkUntil    time.Time
	parkServices []string

	// speedMu guards speedSamples, the per-job byte-rate sampling that makes
	// mode=queue's kbpersec a measurement instead of a guess.
	speedMu      sync.Mutex
	speedSamples map[string]speedSample

	// inFlight holds one entry per nzo_id that currently has a worker
	// goroutine. See dispatchJob.
	inFlight sync.Map
}

// speedSample is one job's (bytes remaining, when observed) pair, kept
// between queue polls so the next poll can turn two samples into a rate.
type speedSample struct {
	sizeleft int64
	at       time.Time
}

func NewHandler(q *queue.SQLiteQueue, client *spotiflac.Client, s *storage.Storage, cfg *config.Config, version string) *Handler {
	h := &Handler{
		queue:     q,
		client:    client,
		storage:   s,
		cfg:       cfg,
		version:   version,
		log:       zerolog.Nop(),
		sem:       make(chan struct{}, maxConcurrent),
		breaker:   breaker.New(5, 10*time.Minute),
		breakGate: newUpstreamBreakGate(zerolog.Nop()),
	}
	if cfg.MaxConcurrent > 0 {
		h.sem = make(chan struct{}, cfg.MaxConcurrent)
	}
	return h
}

// SetVerifyStore wires the pending-community-verification store so
// attemptDownload can record a link for mode=warnings to surface. Optional:
// nil is fine and just means verification links never get recorded (the
// download still fails the same way once its CLI-side timeout elapses).
func (h *Handler) SetVerifyStore(store *verify.Store) {
	h.verifyStore = store
}

// SetBackendWarnings wires the check mode=warnings runs for a download backend
// that cannot deliver (health.BackendWarnings). Optional: nil reports nothing.
func (h *Handler) SetBackendWarnings(f func() []health.BackendWarning) {
	h.backendWarnings = f
}

func (h *Handler) SetLogger(log zerolog.Logger) {
	h.log = log
	h.breakGate.setLogger(log)
}

func (h *Handler) RegisterRoutes(app *fiber.App) {
	h.RegisterRoutesOnGroup(app.Group("/api/sabnzbd"))
}

func (h *Handler) RegisterRoutesOnGroup(group fiber.Router) {
	group.Get("/", h.dispatch)
	group.Post("/", h.dispatch)
}

func (h *Handler) dispatch(c fiber.Ctx) error {
	mode := c.Query("mode")
	if mode == "" {
		mode = c.FormValue("mode")
	}

	handlers := map[string]func(fiber.Ctx) error{
		"version":        h.handleVersion,
		"auth":           h.handleAuth,
		"get_config":     h.handleGetConfig,
		"get_cats":       h.handleGetCats,
		"fullstatus":     h.handleFullStatus,
		"addurl":         h.handleAddURL,
		"addfile":        h.handleAddURL,
		"queue":          h.handleQueueDispatch,
		"history":        h.handleHistory,
		"change_cat":     h.handleChangeCat,
		"server_stats":   h.handleServerStats,
		"status":         h.handleStatus,
		"retry":          h.handleRetry,
		"warnings":       h.handleWarnings,
		"pause_all":      h.handlePauseAll,
		"resume_all":     h.handleResumeAll,
		"set_speedlimit": h.handleSetSpeedlimit,
	}

	fn, ok := handlers[mode]
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(sabnzbd.StatusResponse{
			Status: false,
			Error:  fmt.Sprintf("unknown mode: %s", mode),
		})
	}
	return fn(c)
}

// handleQueueDispatch covers the SABnzbd quirk where "queue" is overloaded
// with a `name` sub-action (pause/resume/delete) instead of the actual
// queue listing, which is the default when name is unset/unrecognized.
func (h *Handler) handleQueueDispatch(c fiber.Ctx) error {
	switch c.Query("name") {
	case "pause":
		return h.handlePause(c)
	case "resume":
		return h.handleResume(c)
	case "delete":
		return h.handleDelete(c)
	default:
		return h.handleQueue(c)
	}
}

func (h *Handler) handleChangeCat(c fiber.Ctx) error {
	nzoID := c.Query("value")
	// Real SABnzbd names the new category "cat"; value2 is kept for
	// backwards compatibility with earlier callers.
	newCat := c.Query("value2")
	if newCat == "" {
		newCat = c.Query("cat")
	}
	if nzoID == "" || newCat == "" {
		return c.Status(fiber.StatusBadRequest).JSON(sabnzbd.StatusResponse{
			Status: false, Error: "missing value/value2",
		})
	}
	job, err := h.queue.Get(nzoID)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(sabnzbd.StatusResponse{
			Status: false, Error: "job not found",
		})
	}
	job.Category = newCat
	svc, qual := config.ParseCategory(newCat)
	if svc != "" {
		job.Service = svc
	}
	if qual != "" {
		job.Quality = qual
	}
	if err := h.queue.Update(job); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(sabnzbd.StatusResponse{
			Status: false, Error: err.Error(),
		})
	}
	return c.JSON(sabnzbd.StatusResponse{Status: true})
}

// ProcessDownloadSync runs the download synchronously. Production call sites
// use dispatch(); tests call this directly.
func (h *Handler) ProcessDownloadSync(job *queue.Job) {
	h.processDownload(job)
}

// jobRun is the per-nzo_id worker bookkeeping: whether something asked the
// worker to run the job again once its current pass finishes. The cancel func
// for a running job lives in h.running, where mode=delete and mode=pause look
// for it.
type jobRun struct {
	mu    sync.Mutex
	rerun bool
}

// askAgain marks the worker for a re-run once its current pass finishes.
func (r *jobRun) askAgain() {
	r.mu.Lock()
	r.rerun = true
	r.mu.Unlock()
}

// takeRerun consumes the pending re-run flag.
func (r *jobRun) takeRerun() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	again := r.rerun
	r.rerun = false
	return again
}

// dispatchJob runs a job in the background, at most one worker at a time.
//
// Every route that can move a job towards "running" used to spawn the worker
// itself (`go h.ProcessDownloadSync(job)`) - addurl, queue resume,
// resume_all, retry, both requeue paths and ResumeQueuedJobs - with nothing
// checking whether a worker for that job was already alive. Two workers on
// one job share its output directory and its row: they can both take a
// concurrency slot, and when the first one finishes and moves the row to
// history the second one's next progress write sets status=Downloading on a
// historical row, which RecoverStuckJobs never repairs (it only looks at
// is_history = 0), so Lidarr shows it as downloading forever.
//
// The worker RELOADS the job by nzo_id rather than using the caller's struct,
// and it loops there instead of spawning a second goroutine. Both matter:
//
//   - The requeue paths call dispatchJob from inside the worker that is about
//     to return (a cooldown or a never-attempted park sends the job back).
//     With a plain "already in flight, refuse" guard that re-dispatch was
//     dropped on the floor and the job sat Queued until the next restart;
//     with a plain "spawn anyway" the caller and the new worker would both
//     hold the same *queue.Job (a data race, found by -race in CI) and the
//     finished worker's cleanup would unregister its successor.
//   - Reloading means a worker owns its own copy, and a job deleted while it
//     waited is simply not found.
func (h *Handler) dispatchJob(job *queue.Job) {
	h.dispatchNzoID(job.NzoID)
}

func (h *Handler) dispatchNzoID(nzoID string) {
	state, loaded := h.inFlight.LoadOrStore(nzoID, &jobRun{})
	run := state.(*jobRun)
	if loaded {
		h.log.Debug().Str("nzo_id", nzoID).
			Msg("job already has a worker; asking it to run again when it finishes")
		run.askAgain()
		return
	}
	go h.runJob(nzoID, run)
}

// runJob is one nzo_id's worker: it processes the job, and processes it again
// for as long as something kept asking.
func (h *Handler) runJob(nzoID string, state *jobRun) {
	for {
		job, err := h.queue.Get(nzoID)
		if err != nil {
			// Deleted, or already in history: there is nothing to run.
			h.retireJob(nzoID, state)
			return
		}
		h.ProcessDownloadSync(job)
		if !state.takeRerun() && h.retireJob(nzoID, state) {
			return
		}
	}
}

// retireJob removes the in-flight entry and reports whether the worker may
// exit. It answers false - leaving the worker in place to run the job once
// more - when a re-run arrived between the worker's last check and this call,
// because deregistering first would drop that request on the floor and leave
// the job Queued until the next restart.
func (h *Handler) retireJob(nzoID string, state *jobRun) bool {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.rerun {
		state.rerun = false
		return false
	}
	// CompareAndDelete, not Delete: only ever retire our own registration.
	h.inFlight.CompareAndDelete(nzoID, state)
	return true
}

// ResumeQueuedJobs re-dispatches every job still sitting in Queued or
// Paused, and returns how many it started. Called once at startup.
//
// A job's worker goroutine dies with the process, but the row stays where it
// was, and nothing ever looked at it again -- so any job that had not yet
// reached Downloading when the container restarted was stranded
// permanently. Found in production 2026-08-07: 13 jobs queued on 2026-08-03
// and -04 were still listed as Queued four days and several restarts later.
// Lidarr sees them as pending forever -- they never download, never fail,
// and never time out.
//
// Paused counts as stranded too: mode=pause sets the row's status and does
// not stop the running worker, so after a restart nothing else would ever
// pick a Paused row up either.
//
// The list is paged to exhaustion. queue.List defaults Limit to 50, and the
// old single call therefore resumed at most 50 rows while reporting that
// number as the size of the backlog - silently stranding the 51st and later
// jobs, which is the very bug this function exists to fix.
func (h *Handler) ResumeQueuedJobs() int {
	const page = 200
	started := 0
	for start := 0; ; start += page {
		jobs, total, err := h.queue.List(queue.ListParams{Start: start, Limit: page})
		if err != nil {
			h.log.Error().Err(err).Msg("resume queued jobs: list failed")
			return started
		}
		for _, job := range jobs {
			if job.Status != sabnzbd.StatusQueued && job.Status != sabnzbd.StatusPaused {
				continue
			}
			h.dispatchJob(job)
			started++
		}
		if len(jobs) < page || start+page >= total {
			break
		}
	}
	return started
}

// parkForUpstreamBreak holds the job in its Queued state until the upstream
// community break window has elapsed. The job stays Queued while parked,
// which is what it is - Lidarr sees a pending download, not a failure.
// Logged at debug, not info: with SPF_MAX_CONCURRENT=1 a deep backlog walks
// this function one job after another while parked, and at info level each
// job wrote its own line every few seconds - observed 2026-08-27: ~50 queued
// jobs produced one line per job per poll cycle and buried the events worth
// finding. The break window is itself a single event: the gate logs it when
// it opens or extends, mode=warnings carries the remaining pause for
// machine and human consumers, and /health mirrors both the break window
// and the session expiry.
func (h *Handler) parkForUpstreamBreak(ctx context.Context, job *queue.Job) {
	if r := h.breakGate.remaining(); r > 0 {
		h.log.Debug().Str("nzo_id", job.NzoID).Dur("pause", r).Msg("upstream community break active; holding job in queue")
	}
	h.breakGate.waitContext(ctx)
}

// HealthExtras reports the states that decide whether a queued backlog can
// drain at all, as machine-readable fields for /health: the remaining
// upstream community break pause (epoch seconds, 0 when not paused), the
// CLI community session's expiry (RFC3339, null when no valid session), and
// the circuit park (epoch seconds, 0 when no service circuit is holding the
// queue).
//
// Before the park was here, a container that was healthy by every measure
// this endpoint reports could sit on a full queue for an hour with nothing
// explaining it - and the external stuck-monitor, seeing slots and no speed,
// restarted it.
func (h *Handler) HealthExtras() map[string]interface{} {
	resp := map[string]interface{}{}
	if r := h.breakGate.remaining(); r > 0 {
		resp["break_until"] = time.Now().Add(r).Unix()
	} else {
		resp["break_until"] = int64(0)
	}
	if r, _ := h.circuitParkState(); r > 0 {
		resp["circuit_park_until"] = time.Now().Add(r).Unix()
	} else {
		resp["circuit_park_until"] = int64(0)
	}
	var sessionExpiry interface{}
	if valid, exp := spotiflac.SessionState(); valid {
		sessionExpiry = exp.UTC().Format(time.RFC3339)
	}
	resp["session_expires_at"] = sessionExpiry
	return resp
}

const maxAttempts = 3

var retryBackoff = []time.Duration{5 * time.Second, 15 * time.Second}

// jobContext derives the context that bounds a job's PROCESSING time from
// the caller's cancellable parent. The returned func undoes it and must be
// deferred.
//
// The deadline is the whole wall-clock budget - two JobTimeouts, one slow
// attempt plus one retry - and it starts HERE, at processing start, not at
// TimeAdded. Measuring from TimeAdded meant a job that sat queued through an
// outage arrived at its slot already out of time and failed instantly:
// observed 2026-08-22, a ~30-job backlog draining into mass "budget
// exhausted" failures minutes after the API recovered. The budget still
// bounds what it is for, active slot occupancy. Every phase derives its own
// deadline from this context, so a dead backend cannot consume the budget and
// leave the fallback phases with an already-expired one - which is exactly
// how outages used to surface: Python burned the full 30m, then "start
// spotiflac: context deadline exceeded" on the CLI, then "job budget
// exhausted" on every fallback.
func (h *Handler) jobContext(parent context.Context, nzoID string) (context.Context, func()) {
	if h.cfg.JobTimeout <= 0 {
		ctx, cancel := context.WithCancel(parent)
		return ctx, cancel
	}
	ctx, cancelDeadline := context.WithDeadline(parent, time.Now().Add(2*h.cfg.JobTimeout))
	ctx, cancel := context.WithCancel(ctx)
	return ctx, func() {
		cancel()
		cancelDeadline()
	}
}

// beginDownload allocates the job's output directory and marks it
// Downloading, returning the directory. A failure to persist the status is
// logged rather than fatal: the download can still run, and the next
// queue.Update carries the state forward.
func (h *Handler) beginDownload(job *queue.Job) (string, error) {
	jobDir, err := h.storage.PrepareJobDir(job.NzoID)
	if err != nil {
		return "", err
	}
	job.Status = sabnzbd.StatusDownloading
	job.OutputPath = jobDir
	// The job's own clock starts here, not at TimeAdded - a job can wait
	// hours in the queue, and every "has this been running too long" check
	// has to measure the run, not the wait.
	now := time.Now()
	job.StartedAt = &now
	job.ProgressAt = &now
	if err := h.queue.Update(job); err != nil {
		h.log.Error().Err(err).Str("nzo_id", job.NzoID).Msg("mark job downloading failed")
	}
	return jobDir, nil
}

func (h *Handler) processDownload(job *queue.Job) {
	// Cancel registration comes FIRST, before any wait. It used to happen
	// after the concurrency slot was taken, so deleting or pausing a job
	// that was still queued or parked was a no-op: the row disappeared and
	// the orphaned goroutine later took the slot, re-created the output
	// directory and downloaded for up to the full budget into a row that no
	// longer existed - with SPF_MAX_CONCURRENT=1 that is the whole queue
	// stalled behind a job nobody is waiting for any more.
	//
	// This context carries no deadline: the job's wall-clock budget starts
	// when it starts PROCESSING, not while it waits its turn, or a job that
	// queued through an outage would arrive at its slot already expired.
	waitCtx, cancelWait := context.WithCancel(context.Background())
	h.running.Store(job.NzoID, cancelWait)
	defer func() {
		h.running.Delete(job.NzoID)
		cancelWait()
	}()

	// Community-session renewal, before the first wait. The community tier
	// is what actually delivers audio while the custom Tidal APIs are down,
	// and it needs a desktop session minted by solving a Turnstile
	// challenge. Until this hook existed, an expired session meant every job
	// failed its verification tier until a human ran a solver by hand.
	// No-op unless SPF_SESSION_RENEW_CMD is configured, and rate-limited
	// inside the client.
	h.client.EnsureCommunitySession()

	// Park BEFORE taking a concurrency slot - a wait inside the semaphore
	// would hold one of SPF_MAX_CONCURRENT slots for the whole pause and
	// wedge the queue - then take the slot and check AGAIN.
	//
	// The second check is the fix for the last remaining way a job could die
	// with "circuit open". Parking happens before the semaphore, but the
	// wait for the semaphore is unbounded: with SPF_MAX_CONCURRENT=1 and a
	// backlog, a job that finished parking could still wait the better part
	// of an hour for the slot, and by then the services it was waiting on
	// had been re-opened by the jobs that ran ahead of it. Measured
	// 2026-10-02, job SABnzbd_nzo_33ed151d-eb2: parked 12:59:41 with
	// retry_in 9m, failed 14:03:22 with "service tidal temporarily
	// unavailable (circuit open)" and not one attempt logged in between.
	// Releasing the slot and looping keeps the guarantee the park makes:
	// an open circuit never turns into a failed grab.
	for {
		if waitCtx.Err() != nil {
			return // deleted or paused while queued; the row is gone or idle
		}
		h.parkForUpstreamBreak(waitCtx, job)
		h.parkForOpenCircuits(waitCtx, job)
		select {
		case h.sem <- struct{}{}:
		case <-waitCtx.Done():
			return
		}
		if h.anyCandidateAllowed(job) {
			break
		}
		<-h.sem
	}
	defer func() { <-h.sem }()

	ctx, releaseCtx := h.jobContext(waitCtx, job.NzoID)
	defer releaseCtx()

	primarySvc := job.Service
	out := &jobOutcome{}

	jobDir, err := h.beginDownload(job)
	if err != nil {
		metrics.RecordJobResult(string(sabnzbd.StatusFailed), job.Service)
		h.failJob(job, err.Error())
		return
	}

	// If the primary's breaker is already open, don't attempt it at all --
	// but still fall through to the fallback loop below instead of failing
	// immediately, so a healthy fallback service (if configured) still gets
	// a chance. Only treat "attempted and failed" primaries as a breaker
	// failure to record; an open breaker we skipped isn't a new failure.
	var lastErr string
	switch {
	case !h.client.SupportsService(primarySvc):
		// A service this build cannot serve is a config fact, not a
		// download result: attempting it only produces the same
		// "Python backend not available" string every time. Skip
		// straight to the fallback loop, which is already filtered to
		// services that exist here.
		lastErr = fmt.Sprintf("service %s is not available in this deployment", primarySvc)
		h.log.Warn().Str("nzo_id", job.NzoID).Str("service", primarySvc).
			Msg("primary service unsupported by this build; going straight to fallbacks")
	case !h.breaker.Allow(primarySvc):
		lastErr = fmt.Sprintf("service %s temporarily unavailable (circuit open)", primarySvc)
		if waited := job.CLIOutput; waited != "" {
			lastErr += " - " + waited
		}
		metrics.RecordJobResult(string(sabnzbd.StatusFailed), primarySvc)
	default:
		retryDL := h.client.Download
		if h.client.HasPythonBackend() {
			// The Python cascade's cross-track breaker has already proven every
			// one of its providers dead for this release inside ONE run; re-running
			// the whole Python cascade on retry N repeats the same multi-minute
			// wall (measured 2026-08-21: ~18 min of provider budgets per attempt
			// while the upstream APIs were down). Retries therefore go straight to
			// the CLI backends - a genuinely different code path (custom API URLs,
			// community tier). Deezer (Python-only) is not retried: attempt 1 still
			// gets the full cascade, and during an outage the Python providers are
			// exactly the ones burning the budget.
			retryDL = h.client.DownloadCLI
		}
		var attempted bool
		lastErr, attempted = h.runAttemptsWithRetry(ctx, job, jobDir, maxAttempts, h.client.Download, retryDL)
		if lastErr == "" {
			return
		}
		h.observe(lastErr, attempted, out, primarySvc)
		if ctx.Err() == nil {
			// Live context: the backend itself failed. Whether that opens
			// the breaker depends on the class - see recordServiceFailure.
			class, _ := h.classifyFailure(lastErr)
			h.recordServiceFailure(primarySvc, class)
		} else {
			// The job's own budget/cancellation ended it, not the service.
			// Recording these tripped the breaker right after an outage
			// lifted (observed 2026-08-22: five budget-exhausted jobs opened
			// the tidal breaker and fast-failed 36 fresh jobs with "circuit
			// open" while the API was healthy).
			h.log.Warn().Str("nzo_id", job.NzoID).Str("service", primarySvc).Msg("job ended by budget/cancel; not recording service failure")
		}
		metrics.RecordJobResult(string(sabnzbd.StatusFailed), primarySvc)
	}

	// Per-service fallback. When the Python backend is available its internal
	// cascade has ALREADY tried every configured service for this release,
	// so re-running it once per fallback service only repeats the same
	// failures while burning the wall-clock budget. The CLI backends are a
	// different code path (custom API URLs, hifi adapter, FSL solving) and
	// are what this loop exists to try - one CLI attempt per service.
	fallbackDownload := h.client.Download
	if h.client.HasPythonBackend() {
		fallbackDownload = h.client.DownloadCLI
	}

	if done := h.runFallbackChain(ctx, job, jobDir, fallbackDownload, out, &lastErr); done {
		return
	}

	h.concludeFailedAttempts(ctx, job, lastErr, out)
}

// runFallbackChain walks the configured services after the primary. It
// reports true when one of them succeeded (the job is already in history).
//
// Split out of processDownload, which had grown past the linter's complexity
// budget: the chain adds a branch per service per outcome on top of an
// already long primary path.
func (h *Handler) runFallbackChain(ctx context.Context, job *queue.Job, jobDir string, fallbackDownload downloadFn, out *jobOutcome, lastErr *string) bool {
	for _, fallbackSvc := range h.fallbackChain(job.Service) {
		// Upstream announced a break while the primary was running. Every
		// further request is the hammering the announcement asks us to stop,
		// and the service that would answer it is the one that just told us
		// to go away.
		if out.cooldown > 0 {
			break
		}
		if !h.breaker.Allow(fallbackSvc) {
			continue
		}
		if ctx.Err() != nil {
			h.log.Info().Str("nzo_id", job.NzoID).Msg("job canceled or budget expired, stopping service fallback")
			// Break, never return: returning here skipped failJob and left the
			// row in Downloading forever - Lidarr saw a pending download that
			// never moved, the queue slot held its concurrency slot, and only a
			// container restart (RecoverStuckJobs) ever cleared it. Observed in
			// production 2026-08-21: a job sat "Downloading" at 0 B/s for hours
			// after its wall-clock budget expired. Breaking lets the wrap-up
			// below mark the job Failed and move it to history.
			break
		}
		fbErr, attempted := h.tryFallbackService(ctx, job, jobDir, fallbackSvc, fallbackDownload)
		if fbErr == "" {
			return true
		}
		// The most recent attempt's reason is the one that explains the
		// job's fate; the primary's is only a fallback for when nothing ran.
		*lastErr = fbErr
		stop := h.observe(fbErr, attempted, out, fallbackSvc)
		if ctx.Err() == nil {
			// Same rule as the primary path above: a dead context means the
			// job's own budget/cancellation ended the attempt, not a service
			// failure - do not feed it to the breaker.
			class, _ := h.classifyFailure(fbErr)
			h.recordServiceFailure(fallbackSvc, class)
		}
		metrics.RecordJobResult(string(sabnzbd.StatusFailed), fallbackSvc)
		if stop {
			break
		}
	}
	return false
}

// anyCandidateAllowed reports whether at least one service this job could
// use is currently admitted by its circuit breaker.
func (h *Handler) anyCandidateAllowed(job *queue.Job) bool {
	candidates := h.candidateServices(job)
	if len(candidates) == 0 {
		// Nothing this deployment can serve. There is no circuit to wait
		// for; let the normal path produce the permanent, honest error.
		return true
	}
	for _, svc := range candidates {
		if h.breaker.RetryAfter(svc) <= 0 {
			return true
		}
	}
	return false
}

// concludeFailedAttempts decides what a job that survived every backend
// without succeeding actually becomes: requeued, or failed.
//
// The decision is made from the WHOLE attempt chain (jobOutcome), not from
// the last error alone. That distinction is the fix for the 2026-10-01
// incident: the gate used to look only at the final error, so a chain whose
// first two services announced a 79-minute scheduled break and whose third
// failed with an unrelated "Amazon API returned status 404" never parked
// anything, and 18 jobs drained into Lidarr's failed history behind a dead
// upstream. Now an announcement anywhere in the chain parks the queue and
// sends the job back, whatever the last service said.
//
// Failing such a job would be a lie: the release was never tried against a
// working API, and once the item is in Lidarr's history only an external
// re-grab cycle brings it back.
func (h *Handler) concludeFailedAttempts(ctx context.Context, job *queue.Job, lastErr string, out *jobOutcome) {
	budgetExhausted := errors.Is(ctx.Err(), context.DeadlineExceeded)
	if budgetExhausted {
		lastErr = fmt.Sprintf("job wall-clock budget (%s) exhausted: %s", 2*h.cfg.JobTimeout, lastErr)
	}

	// A path that never invoked a backend has not learned anything about the
	// release, so it must never be the reason a grab dies. Two ways to get
	// here: every candidate circuit was open (the window between the
	// post-semaphore check and the attempt), or upstream announced a break
	// before anything ran. Hand the job back to the queue instead.
	if ctx.Err() == nil && !out.attempted {
		candidates := h.candidateServices(job)
		if len(candidates) > 0 {
			if h.requeueUnattempted(job, lastErr, candidates) {
				return
			}
		}
	}

	if out.cooldown > 0 && !budgetExhausted {
		if requeued, giveUp := h.requeueAfterCooldown(job, out.cooldown, lastErr); requeued {
			return
		} else if giveUp != "" {
			lastErr = giveUp
		}
	}
	if cooldown, isCooldown := h.breakGate.cooldownFor(lastErr); isCooldown {
		h.breakGate.extend(cooldown)
		if requeued, giveUp := h.requeueAfterCooldown(job, cooldown, lastErr); requeued {
			return
		} else if giveUp != "" {
			lastErr = giveUp
		}
	}
	h.failJob(job, lastErr)
}

// maxUnattemptedRequeues bounds how often one job may be handed back without
// a single attempt. The circuit park already waits up to maxCircuitPark, so
// reaching this at all means the breakers were re-opened by other jobs
// during the wait; a small budget is enough, and unbounded requeueing would
// hide a permanently broken release as an eternally pending download.
const maxUnattemptedRequeues = 5

// requeueUnattempted returns a never-tried job to Queued and re-dispatches
// it, so it re-enters parkForOpenCircuits - which runs BEFORE the semaphore,
// so a waiting job holds no slot. Reports whether the job was requeued;
// false means the caller should fail it (budget spent, or the write failed).
func (h *Handler) requeueUnattempted(job *queue.Job, lastErr string, candidates []string) bool {
	n := h.bumpRequeue(job.NzoID)
	if n > maxUnattemptedRequeues {
		h.log.Warn().Str("nzo_id", job.NzoID).Int("requeues", n-1).
			Msg("never-attempted requeue budget exhausted; failing job with its reason")
		return false
	}
	job.Status = sabnzbd.StatusQueued
	job.ErrorMessage = ""
	job.Percentage = 0
	job.CompletedAt = nil
	job.StartedAt = nil
	if err := h.queue.Update(job); err != nil {
		h.log.Error().Err(err).Str("nzo_id", job.NzoID).Msg("requeue never-attempted job: update failed")
		return false
	}
	h.log.Warn().Str("nzo_id", job.NzoID).Int("requeue", n).Strs("services", candidates).
		Str("reason", lastErr).
		Msg("job was never attempted (every candidate circuit open); requeuing instead of failing it")
	h.dispatchJob(job)
	return true
}

// maxCooldownRequeues bounds how often one job may be put back for an
// upstream cooldown before it is failed for real.
//
// It has to outlast a real outage, and 3 did not: an announced break parks the
// whole queue, and with SPF_MAX_CONCURRENT=1 the jobs drain one at a time
// between windows, so a single job collects a requeue per break it happens to
// be running through. Measured 2026-10-02: spotbye announced 79-, 120- and
// 118-minute breaks in one evening and the largest job was on its third
// requeue with the backlog barely touched - the next one would have failed it
// with "the server is taking a scheduled short break" as its whole error,
// which is precisely the "downloads fail" symptom this classification work
// exists to remove. 20 requeues is roughly two days of two-hour windows.
//
// The bound still exists because unbounded requeueing would hide a release
// that can never be evaluated as an eternally pending download. It is
// per-process: a restart resets it.
const maxCooldownRequeues = 20

// requeueAfterCooldown returns the job to Queued and re-dispatches it, so it
// waits out the park window in parkForUpstreamBreak - which runs BEFORE the
// concurrency semaphore, so a waiting job holds no slot. Reports whether the
// job was requeued; false means the caller should fail it.
func (h *Handler) requeueAfterCooldown(job *queue.Job, cooldown time.Duration, lastErr string) (requeued bool, giveUp string) {
	n := h.bumpRequeue(job.NzoID)
	if n > maxCooldownRequeues {
		h.log.Warn().Str("nzo_id", job.NzoID).Int("requeues", n-1).
			Msg("upstream cooldown requeue budget exhausted; failing job for real")
		// Say WHY it died. The caller would otherwise report the upstream's
		// own "taking a scheduled short break" message, which reads as if the
		// release were broken; the row has to say that the proxy spent a day
		// retrying an outage instead.
		return false, fmt.Sprintf("gave up after %d upstream cooldowns: %s", n-1, lastErr)
	}
	job.Status = sabnzbd.StatusQueued
	job.ErrorMessage = ""
	job.Percentage = 0
	job.CompletedAt = nil
	job.StartedAt = nil
	if err := h.queue.Update(job); err != nil {
		h.log.Error().Err(err).Str("nzo_id", job.NzoID).Msg("requeue after cooldown: update failed")
		return false, ""
	}
	h.log.Warn().Str("nzo_id", job.NzoID).Int("requeue", n).Dur("cooldown", cooldown).
		Str("upstream_error", lastErr).Msg("upstream asked us to back off; requeuing job instead of failing it")
	h.dispatchJob(job)
	return true, ""
}

// bumpRequeue increments and returns the requeue count for an nzo_id.
func (h *Handler) bumpRequeue(nzoID string) int {
	h.requeueMu.Lock()
	defer h.requeueMu.Unlock()
	if h.requeues == nil {
		h.requeues = make(map[string]int)
	}
	h.requeues[nzoID]++
	return h.requeues[nzoID]
}

// clearRequeues drops the requeue counter for a job that reached a terminal
// state, so the map does not grow for the process lifetime.
func (h *Handler) clearRequeues(nzoID string) {
	h.requeueMu.Lock()
	defer h.requeueMu.Unlock()
	delete(h.requeues, nzoID)
}

// runAttemptsWithRetry runs up to `attempts` tries of the download via dl,
// sleeping with backoff and clearing the job dir between them. Returns ""
// on success, the last error otherwise, and whether any backend invocation
// actually ran (a job that ran none has learned nothing about the release).
type downloadFn func(ctx context.Context, url, outputDir, service, quality string) (<-chan spotiflac.ProgressEvent, <-chan error)

// first and retry may differ: the caller hands the full Python+CLI cascade
// for attempt 1 and a CLI-only backend for retries (see processDownload),
// because the Python cascade's own breaker already proved its providers dead
// for this release by the end of attempt 1.
//
// Retrying stops early when the error is not a service-side failure. An
// announced upstream break does not change by asking again three seconds
// later - it is upstream telling the whole queue to wait - and a
// release-specific resolution failure produces the same answer every time.
// Measured 2026-10-01: three tidal attempts plus two fallback services for
// each of ~18 jobs, all of it against an upstream that had announced a
// 79-minute break.
func (h *Handler) runAttemptsWithRetry(ctx context.Context, job *queue.Job, jobDir string, attempts int, first, retry downloadFn) (string, bool) {
	var lastErr string
	attempted := false
	dl := first
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			dl = retry
		}
		if ctx.Err() != nil {
			if attempt > 1 {
				h.log.Warn().Str("nzo_id", job.NzoID).Int("attempt", attempt).Msg("job budget exhausted, not retrying")
			}
			return "canceled", attempted
		}
		ok, errMsg := h.attemptDownload(ctx, job, jobDir, dl)
		attempted = true
		if !ok {
			// A failed attempt's backend process can outlive its terminal
			// error event (still waiting on a verification callback or
			// finishing remaining tracks). Left alone it races the next
			// attempt - same job dir, shared bolt state files - so kill it.
			h.client.AbortActive(jobDir)
		}
		if ok {
			return "", true
		}
		lastErr = errMsg
		if attempt < attempts {
			if !h.shouldRetry(errMsg) {
				h.log.Warn().Str("nzo_id", job.NzoID).Int("attempt", attempt).
					Str("error", errMsg).
					Msg("not retrying: this failure is not a service-side one")
				break
			}
			h.log.Warn().Str("nzo_id", job.NzoID).Int("attempt", attempt).Str("error", errMsg).Msg("download attempt failed, retrying")
			if cerr := h.storage.CleanupJob(job.NzoID); cerr != nil {
				h.log.Warn().Err(cerr).Str("nzo_id", job.NzoID).Msg("failed to clean up job dir before retry")
			} else if _, perr := h.storage.PrepareJobDir(job.NzoID); perr != nil {
				h.log.Warn().Err(perr).Str("nzo_id", job.NzoID).Msg("failed to recreate job dir before retry")
			}
			time.Sleep(retryBackoff[attempt-1])
		}
	}
	return lastErr, attempted
}

// tryFallbackService switches the job over to svc, resets its job dir and
// runs one download attempt through dl. Returns "" on success (the job has
// been moved to history by attemptDownload), the error otherwise, and
// whether a backend invocation actually ran.
func (h *Handler) tryFallbackService(ctx context.Context, job *queue.Job, jobDir, svc string, dl downloadFn) (string, bool) {
	h.log.Warn().Str("nzo_id", job.NzoID).Str("from_service", job.Service).Str("to_service", svc).Msg("falling back to next service")
	job.Service = svc
	if err := h.queue.Update(job); err != nil {
		h.log.Error().Err(err).Str("nzo_id", job.NzoID).Msg("record fallback service failed")
	}
	if cerr := h.storage.CleanupJob(job.NzoID); cerr != nil {
		h.log.Warn().Err(cerr).Str("nzo_id", job.NzoID).Msg("failed to clean up job dir before fallback attempt")
	} else if _, perr := h.storage.PrepareJobDir(job.NzoID); perr != nil {
		h.log.Warn().Err(perr).Str("nzo_id", job.NzoID).Msg("failed to recreate job dir before fallback attempt")
	}
	return h.runAttemptsWithRetry(ctx, job, jobDir, 1, dl, dl)
}

// fallbackChain returns the configured fallback services after the given
// current service, preserving configured order, excluding the current one
// and excluding anything this deployment cannot serve at all.
//
// The exclusion matters: SPF_FALLBACK_SERVICES here is "qobuz,deezer,amazon"
// while the image ships no Python backend, so deezer consumed a fallback
// slot and returned a deployment fact ("only available through the Python
// backend") dressed up as a download failure. Measured 2026-09-10: 7 such
// failures in the retained history, every one of them unretryable by
// construction.
func (h *Handler) fallbackChain(current string) []string {
	var chain []string
	for _, svc := range h.cfg.FallbackServices {
		if svc == current {
			continue
		}
		if !h.client.SupportsService(svc) {
			continue
		}
		chain = append(chain, svc)
	}
	return chain
}

// candidateServices is every service this job could still be served by, in
// attempt order: the primary first, then the surviving fallback chain.
func (h *Handler) candidateServices(job *queue.Job) []string {
	out := make([]string, 0, len(h.cfg.FallbackServices)+1)
	if h.client.SupportsService(job.Service) {
		out = append(out, job.Service)
	}
	return append(out, h.fallbackChain(job.Service)...)
}

// maxCircuitPark bounds how long a job may sit parked waiting for a circuit
// to close. The breaker cooldown is 10 minutes, so a park normally resolves
// well inside this; the cap only exists so a pathological breaker cannot
// hold a job forever without ever reaching a real attempt.
var maxCircuitPark = 45 * time.Minute

// parkForOpenCircuits holds the job in Queued while EVERY service it could
// use has an open breaker, and returns once at least one is allowed again.
//
// Without this an open circuit was terminal: processDownload skipped the
// primary, found every fallback breaker open too, and fell straight through
// to failJob with "service tidal temporarily unavailable (circuit open)" as
// the entire error. With SPF_MAX_CONCURRENT=1 and a backlog, a ten-minute
// provider hiccup therefore drained the whole queue into Lidarr's failed
// history in seconds. Measured 2026-09-10 over the retained 500-slot
// history: 151 of 161 failures were exactly that, in bursts (34 on 08-27,
// 46 on 08-31, 33 on 09-04) - none of them a download that was attempted
// and lost.
//
// A breaker protects the upstream. Parking honors that (zero requests
// reach the open service) while keeping the job honest to Lidarr: it stays
// Queued, which is what it is. Same shape as parkForUpstreamBreak, and for
// the same reason it runs BEFORE the concurrency semaphore - waiting inside
// the semaphore would wedge the single slot for the whole cooldown.
func (h *Handler) parkForOpenCircuits(ctx context.Context, job *queue.Job) {
	deadline := time.Now().Add(maxCircuitPark)
	logged := false
	parked := false
	defer func() {
		if parked {
			h.endCircuitPark()
		}
	}()
	for {
		if ctx.Err() != nil {
			return
		}
		candidates := h.candidateServices(job)
		if len(candidates) == 0 {
			// Nothing this deployment can serve; let the normal path
			// produce the (permanent, honest) error.
			return
		}
		wait := maxCircuitPark
		for _, svc := range candidates {
			d := h.breaker.RetryAfter(svc)
			if d <= 0 {
				return // at least one service is available right now
			}
			if d < wait {
				wait = d
			}
		}
		if time.Now().After(deadline) {
			h.log.Warn().Str("nzo_id", job.NzoID).Dur("parked_for", maxCircuitPark).
				Strs("services", candidates).
				Msg("circuit park cap reached; attempting anyway")
			// Remember that we waited. Without this the eventual failure
			// reads "service tidal temporarily unavailable (circuit open)",
			// identical to the old fail-fast behavior, and the next person
			// to look cannot tell a job that gave up after 45 minutes from
			// one that was never tried.
			job.CLIOutput = fmt.Sprintf("parked %s waiting for a closed circuit on %v before this attempt",
				maxCircuitPark, candidates)
			// "Attempting anyway" has to mean an actual attempt. Every
			// downstream path asks the breaker first, so without closing at
			// least the primary circuit the cap produced exactly the
			// terminal "circuit open" failure this park exists to prevent.
			// A failure after the reset re-opens the breaker normally.
			h.breaker.Reset(job.Service)
			return
		}
		parked = true
		h.beginCircuitPark(candidates, wait)
		if !logged {
			h.log.Info().Str("nzo_id", job.NzoID).Strs("services", candidates).Dur("retry_in", wait).
				Msg("every candidate service circuit is open; holding job in queue")
			logged = true
		}
		if wait > circuitParkPoll {
			wait = circuitParkPoll
		}
		time.Sleep(wait)
	}
}

// circuitParkPoll caps a single park sleep so a breaker that closes early
// (RecordSuccess from another job) is noticed promptly.
var circuitParkPoll = 15 * time.Second

// beginCircuitPark / endCircuitPark publish "the queue is deliberately
// holding work while service circuits cool down" so the state is visible
// from outside the process.
//
// It has to be visible, because from outside it is indistinguishable from a
// hang: slots in the queue, speed at 0, no requests going out. The external
// stuck-monitor on potatostack watched exactly those two numbers and
// restarted this container eight times in the week to 2026-10-02, each time
// discarding the breakers, the park and the requeue counters and failing
// whatever was in flight. mode=warnings carries it now (and the monitor
// consults it); /health mirrors it as circuit_park_until.
func (h *Handler) beginCircuitPark(services []string, wait time.Duration) {
	h.parkMu.Lock()
	defer h.parkMu.Unlock()
	h.parkServices = services
	h.parkUntil = time.Now().Add(wait)
}

func (h *Handler) endCircuitPark() {
	h.parkMu.Lock()
	defer h.parkMu.Unlock()
	h.parkServices = nil
	h.parkUntil = time.Time{}
}

// circuitParkState reports how much longer the circuit park is expected to
// last and which services it is waiting on. Zero when nothing is parked.
func (h *Handler) circuitParkState() (time.Duration, []string) {
	h.parkMu.Lock()
	defer h.parkMu.Unlock()
	if r := time.Until(h.parkUntil); r > 0 {
		return r, append([]string(nil), h.parkServices...)
	}
	return 0, nil
}

// attemptDownload runs a single backend invocation and reports whether it
// succeeded. On success it fully updates the job to Completed and moves it
// to history itself (mirroring the previous inline behavior); on failure it
// returns false with the error message and leaves the job untouched for the
// caller to retry or ultimately fail.
func (h *Handler) attemptDownload(ctx context.Context, job *queue.Job, jobDir string, dl downloadFn) (bool, string) {
	// A previous download's browser is still running and will stop this one's
	// from starting at all (see reapStaleBrowsers). Guarded on concurrency
	// because a sibling job's browser is indistinguishable from a stray, so
	// this is only safe when there cannot be a sibling. cap(h.sem), not
	// cfg.MaxConcurrent: NewHandler falls back to a 3-slot semaphore when the
	// configured value is 0, and the old test then believed it had the queue
	// to itself and killed a sibling's browser.
	if cap(h.sem) <= 1 {
		reapStaleBrowsers()
	}

	events, errs := dl(ctx, job.SpotifyURL, jobDir, job.Service, job.Quality)

	// Both channels are drained until BOTH are closed. Returning as soon as
	// `events` closes - as this used to, with the generic "cli exited
	// without completion signal" - lost the backend's real reason about half
	// the time: the producer closes both channels in one defer, so `events`
	// and `errs` are ready together and Go's select picks between them at
	// random. That mattered more than a vague message once the failure
	// classification became string-based (see failure.go): a dropped
	// "scheduled break" meant no park, and a dropped "couldn't find Tidal
	// URL" meant the release was retried and fed to the breaker.
	for events != nil || errs != nil {
		select {
		case evt, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if evt.Type == "complete" {
				return h.handleCompleteEvent(job, evt)
			}
			h.handleProgressEvent(job, evt)
		case e, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if e != nil {
				var de *spotiflac.DownloadError
				if errors.As(e, &de) && de.RawOutput != "" {
					job.CLIOutput = de.RawOutput
				}
				// The backend's own reason lines are the only explanation a
				// failure has. Logging just "spotiflac exited: exit status
				// 1" - which is all this used to emit - leaves nothing to
				// debug with.
				h.log.Warn().
					Str("nzo_id", job.NzoID).
					Str("service", job.Service).
					Str("error", e.Error()).
					Str("detail", lastLines(job.CLIOutput, 12)).
					Msg("download backend reported a failure")
				return false, e.Error()
			}
		}
	}
	return false, "cli exited without completion signal"
}

// handleProgressEvent applies every non-terminal CLI event to the in-memory
// job (persisting where relevant). "complete" is terminal and handled by the
// caller directly; everything else - progress, metadata, and a pending
// community-verification link - just updates state along the way.
func (h *Handler) handleProgressEvent(job *queue.Job, evt spotiflac.ProgressEvent) {
	switch evt.Type {
	case "progress":
		// Bytes written is the better signal and the backend reports it:
		// deriving progress from the finished-file count leaves any
		// single-track release at 0 % until it is done, so Lidarr's queue
		// shows a download with no movement. Fall back to the reported
		// percentage when there is no byte count (or no size to measure
		// against).
		if evt.Bytes > 0 && job.Size > 0 {
			job.Sizeleft = max(job.Size-evt.Bytes, 0)
			job.Percentage = min(100*float64(evt.Bytes)/float64(job.Size), 99)
		} else {
			job.Percentage = evt.Percent
			job.Sizeleft = int64(float64(job.Size) * (100 - evt.Percent) / 100)
		}
		now := time.Now()
		job.ProgressAt = &now
		if err := h.queue.Update(job); err != nil {
			h.log.Error().Err(err).Str("nzo_id", job.NzoID).Msg("progress update failed")
		}
	case "metadata":
		job.Filename = releaseName(job.Filename, evt)
		// A mode=addurl job (a script, not Lidarr) arrives with no track
		// count, so the backend's own resolved count is the only chance to
		// get one - and without it the partial-album check never runs.
		if job.TrackCount == 0 && evt.TrackCount > 0 {
			job.TrackCount = evt.TrackCount
		}
		now := time.Now()
		job.ProgressAt = &now
		if err := h.queue.Update(job); err != nil {
			h.log.Error().Err(err).Str("nzo_id", job.NzoID).Msg("metadata update failed")
		}
	case "verification_required":
		if evt.URL == "" || evt.CB == "" {
			return
		}
		h.log.Warn().Str("nzo_id", job.NzoID).Str("url", evt.URL).Msg("community verification required, see mode=warnings for the link")
		if h.verifyStore != nil {
			h.verifyStore.Set(evt.URL, evt.CB)
		}
		if h.cfg.VerifyNotifyURL != "" {
			message := "Tidal/Qobuz/Amazon verification needed, open to continue: " + evt.URL
			if err := verify.Notify(h.cfg.VerifyNotifyURL, h.cfg.VerifyNotifyTitle, message); err != nil {
				h.log.Warn().Err(err).Msg("verification notify failed")
			}
		}
	}
}

// handleCompleteEvent finalizes a job once the CLI reports its "complete"
// event: verifies the track count for multi-track albums, records metrics,
// marks the job Completed, and moves it to history.
func (h *Handler) handleCompleteEvent(job *queue.Job, evt spotiflac.ProgressEvent) (bool, string) {
	// evt.TrackCount is what the backend actually wrote; counting the files
	// itself is the fallback for a backend that does not report one. Either
	// way a short album is a failure, not a success: handing Lidarr one file
	// out of thirteen makes it import the single track and then report the
	// release as "Has missing tracks" forever.
	// A "complete" event is not proof of anything on its own. spotiflac-cli
	// emits one even after it has failed - observed verbatim, an error and a
	// completion for the same track:
	//
	//	{"message":"track scared: Unknown service: deezer","type":"error"}
	//	{"album":"scared","path":"/downloads/spotiflac/SABnzbd_nzo_...",
	//	 "type":"complete"}
	//
	// Taking that at face value hands Lidarr an empty directory as a finished
	// download, which it then reports as an import failure instead of a
	// download failure - and the release is blocklisted for the wrong reason.
	// Files on disk are the only evidence that counts.
	// A backend that omits the path in its "complete" event would otherwise
	// fail a download that worked: the directory we allocated for the job is
	// the same one we handed the backend, so it is the right fallback.
	outPath := evt.OutputPath
	if outPath == "" {
		outPath = job.OutputPath
	}
	onDisk, cerr := storage.CountAudioFiles(outPath)
	if cerr != nil {
		return false, fmt.Sprintf("cannot verify %s: %s", outPath, cerr)
	}
	if onDisk == 0 {
		return false, "backend reported completion but wrote no audio files"
	}
	if job.TrackCount > 0 {
		gotCount := evt.TrackCount
		if gotCount == 0 {
			gotCount = onDisk
		}
		if gotCount < job.TrackCount {
			return false, fmt.Sprintf("partial album: %d/%d tracks", gotCount, job.TrackCount)
		}
	}
	h.breaker.RecordSuccess(job.Service)
	h.clearRequeues(job.NzoID)
	metrics.RecordJobResult(string(sabnzbd.StatusCompleted), job.Service)
	if !job.TimeAdded.IsZero() {
		metrics.RecordDownloadDuration(job.Service, job.Quality, time.Since(job.TimeAdded).Seconds())
	}
	job.Status = sabnzbd.StatusCompleted
	job.Percentage = 100
	job.Size = evt.Size
	job.Sizeleft = 0
	job.OutputPath = outPath
	now := time.Now()
	job.CompletedAt = &now
	job.Filename = releaseName(job.Filename, evt)
	if err := h.queue.Update(job); err != nil {
		h.log.Error().Err(err).Str("nzo_id", job.NzoID).Msg("mark job completed failed")
	}
	if err := h.queue.MoveToHistory(job.NzoID); err != nil {
		h.log.Error().Err(err).Str("nzo_id", job.NzoID).Msg("move job to history failed")
	}
	h.log.Info().Str("nzo_id", job.NzoID).Str("path", outPath).Msg("download complete")
	return true, ""
}

// releaseName decides what a job is called in queue and history output.
//
// current is whatever the job already carries. On Lidarr's real grab path
// that is the release title it picked, recovered from the synthetic NZB by
// handleAddURL - Lidarr matches its tracked download against that exact
// string, so it always wins. CLI metadata only names a job that arrived
// without one, e.g. a bare mode=addurl call from a script.
//
// The old order put "artist - album" ahead of current, which meant every
// Lidarr grab was renamed the moment the CLI reported metadata. For single
// tracks SpotiFLAC reports no album at all, so that produced names like
// "Fred again.., BLANCO - " - a trailing separator and no title - and Lidarr
// then treated every completed download as untracked and imported none of
// them.
func releaseName(current string, evt spotiflac.ProgressEvent) string {
	if c := strings.TrimSpace(current); c != "" {
		return c
	}

	artist := strings.TrimSpace(evt.Artist)
	album := strings.TrimSpace(evt.Album)
	title := strings.TrimSpace(evt.Title)

	if artist != "" && album != "" {
		return artist + " - " + album
	}
	if artist != "" && title != "" {
		return artist + " - " + title
	}
	if artist != "" {
		return artist
	}
	return title
}

// lastLines returns at most n trailing non-blank lines of s, for logging a
// subprocess's tail without dumping its whole console output.
func lastLines(s string, n int) string {
	lines := []string{}
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, strings.TrimSpace(l))
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

func (h *Handler) failJob(job *queue.Job, errMsg string) {
	h.clearRequeues(job.NzoID)
	job.Status = sabnzbd.StatusFailed
	job.ErrorMessage = errMsg
	now := time.Now()
	job.CompletedAt = &now
	if err := h.queue.Update(job); err != nil {
		h.log.Error().Err(err).Str("nzo_id", job.NzoID).Msg("mark job failed update failed")
	}
	if err := h.queue.MoveToHistory(job.NzoID); err != nil {
		h.log.Error().Err(err).Str("nzo_id", job.NzoID).Msg("move failed job to history failed")
	}
	h.log.Error().Str("nzo_id", job.NzoID).Str("error", errMsg).Msg("download failed")
}

func jobToSlot(job *queue.Job, index int) sabnzbd.Slot {
	return sabnzbd.Slot{
		Status:       string(job.Status),
		Index:        index,
		NzoID:        job.NzoID,
		Filename:     job.Filename,
		Size:         formatBytes(job.Size),
		Sizeleft:     formatBytes(job.Sizeleft),
		Mb:           float64(job.Size) / (1024 * 1024),
		Mbleft:       float64(job.Sizeleft) / (1024 * 1024),
		Mbmissing:    0,
		Percentage:   fmt.Sprintf("%.0f", job.Percentage),
		Timeleft:     formatTimeleft(job.Sizeleft),
		Priority:     sabPriorityName(job.Priority),
		Cat:          job.Category,
		TimeAdded:    job.TimeAdded.Unix(),
		Script:       "Default",
		Unpackopts:   "3",
		AvgAge:       "0d",
		DirectUnpack: "0",
	}
}

// sabPriorityName renders a stored priority the way SABnzbd's API does: as
// the NAME of a SabnzbdPriority member, not the number.
//
// Lidarr deserializes this field with Enum.TryParse over those names, and
// falls back to the enum's zero value when the text is not one of them.
// Lidarr sends the numeric priorities (-100 for its default), so passing the
// number through gave every queue item the wrong priority silently.
func sabPriorityName(p string) string {
	names := map[string]string{
		"-100": "Default",
		"-2":   "Paused",
		"-1":   "Low",
		"0":    "Normal",
		"1":    "High",
		"2":    "Force",
	}
	if name, ok := names[strings.TrimSpace(p)]; ok {
		return name
	}
	for _, name := range names {
		if strings.EqualFold(p, name) {
			return name
		}
	}
	return "Normal"
}

func formatBytes(bytes int64) string {
	if bytes == 0 {
		return "0 B"
	}
	units := []string{"B", "K", "M", "G", "T"}
	size := float64(bytes)
	unitIdx := 0
	for size >= 1024 && unitIdx < len(units)-1 {
		size /= 1024
		unitIdx++
	}
	return fmt.Sprintf("%.2f %s", size, units[unitIdx])
}

func formatTimeleft(sizeleft int64) string {
	if sizeleft == 0 {
		return "0:00:00"
	}
	secs := sizeleft / (1024 * 1024)
	h := secs / 3600
	m := (secs % 3600) / 60
	s := secs % 60
	return fmt.Sprintf("%d:%02d:%02d", h, m, s)
}

func splitComma(s string) []string {
	parts := strings.Split(s, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// firstQuery returns the first non-empty value among the named query params.
// Lidarr's Sabnzbd client sends the queue category filter as "category" while
// real SABnzbd callers (and our own docs) use "cat"; accept both so a queue
// poll filtered by either name behaves identically.
func firstQuery(c fiber.Ctx, names ...string) string {
	for _, n := range names {
		if v := c.Query(n); v != "" {
			return v
		}
	}
	return ""
}
