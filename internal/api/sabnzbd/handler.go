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
	verifyStore *verify.Store

	// running maps an nzo_id to the cancel func of its in-flight download.
	// Deleting a job used to remove the row and leave the goroutine running,
	// so it kept its concurrency slot - with SPF_MAX_CONCURRENT=1 a single
	// abandoned job wedged the whole queue for as long as its retries and
	// service fallbacks took, which is hours. Observed: a freshly added job
	// sat "Queued" for 270s and never started.
	running sync.Map
}

func NewHandler(q *queue.SQLiteQueue, client *spotiflac.Client, s *storage.Storage, cfg *config.Config, version string) *Handler {
	h := &Handler{
		queue:   q,
		client:  client,
		storage: s,
		cfg:     cfg,
		version: version,
		log:     zerolog.Nop(),
		sem:     make(chan struct{}, maxConcurrent),
		breaker: breaker.New(5, 10*time.Minute),
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

func (h *Handler) SetLogger(log zerolog.Logger) {
	h.log = log
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
	newCat := c.Query("value2")
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
// wrap it in `go h.ProcessDownloadSync(job)`; tests call it directly.
func (h *Handler) ProcessDownloadSync(job *queue.Job) {
	h.processDownload(job)
}

// ResumeQueuedJobs re-dispatches every job still sitting in Queued, and
// returns how many it started. Called once at startup.
//
// A job is only ever dispatched from handleAddURL's `go
// h.ProcessDownloadSync(job)`. That goroutine dies with the process, but the
// row stays Queued, and nothing ever looked at it again -- so any job that
// had not yet reached Downloading when the container restarted was stranded
// permanently. queue.RecoverStuckJobs covers the Downloading case (it fails
// them, since partial on-disk state is not trusted); Queued jobs have no
// partial state at all and can simply be started.
//
// Found in production 2026-08-07: 13 jobs queued on 2026-08-03 and -04 were
// still listed as Queued four days and several restarts later. Lidarr sees
// them as pending forever -- they never download, never fail, and never time
// out.
//
// Each dispatch blocks on the same semaphore as a live request, so resuming
// a large backlog cannot exceed SPF_MAX_CONCURRENT.
func (h *Handler) ResumeQueuedJobs() int {
	jobs, _, err := h.queue.List(queue.ListParams{Status: string(sabnzbd.StatusQueued)})
	if err != nil {
		h.log.Error().Err(err).Msg("resume queued jobs: list failed")
		return 0
	}
	for _, job := range jobs {
		go h.ProcessDownloadSync(job)
	}
	return len(jobs)
}

const maxAttempts = 3

var retryBackoff = []time.Duration{5 * time.Second, 15 * time.Second}

func (h *Handler) processDownload(job *queue.Job) {
	h.sem <- struct{}{}
	defer func() { <-h.sem }()

	// The job context carries the whole wall-clock budget: two JobTimeouts
	// (one slow attempt plus one retry), measured from TimeAdded rather than
	// now, so time already spent queued counts against it too. Every phase
	// below derives its own deadline from this context, so a dead backend can
	// no longer consume the budget and leave the fallback phases with an
	// already-expired context - which is exactly how outages used to surface:
	// Python burned the full 30m, then "start spotiflac: context deadline
	// exceeded" on the CLI, then "job budget exhausted" on every fallback.
	parent := context.Background()
	if h.cfg.JobTimeout > 0 && !job.TimeAdded.IsZero() {
		var parentCancel context.CancelFunc
		parent, parentCancel = context.WithDeadline(parent, job.TimeAdded.Add(2*h.cfg.JobTimeout))
		defer parentCancel()
	}
	ctx, cancel := context.WithCancel(parent)
	h.running.Store(job.NzoID, cancel)
	defer func() {
		h.running.Delete(job.NzoID)
		cancel()
	}()

	primarySvc := job.Service

	jobDir, err := h.storage.PrepareJobDir(job.NzoID)
	if err != nil {
		metrics.RecordJobResult(string(sabnzbd.StatusFailed), job.Service)
		h.failJob(job, err.Error())
		return
	}

	job.Status = sabnzbd.StatusDownloading
	job.OutputPath = jobDir
	if err := h.queue.Update(job); err != nil {
		h.log.Error().Err(err).Str("nzo_id", job.NzoID).Msg("mark job downloading failed")
	}

	// If the primary's breaker is already open, don't attempt it at all --
	// but still fall through to the fallback loop below instead of failing
	// immediately, so a healthy fallback service (if configured) still gets
	// a chance. Only treat "attempted and failed" primaries as a breaker
	// failure to record; an open breaker we skipped isn't a new failure.
	var lastErr string
	if !h.breaker.Allow(primarySvc) {
		lastErr = fmt.Sprintf("service %s temporarily unavailable (circuit open)", primarySvc)
		metrics.RecordJobResult(string(sabnzbd.StatusFailed), primarySvc)
	} else {
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
		lastErr = h.runAttemptsWithRetry(ctx, job, jobDir, maxAttempts, h.client.Download, retryDL)
		if lastErr == "" {
			return
		}
		h.breaker.RecordFailure(primarySvc)
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

	for _, fallbackSvc := range h.fallbackChain(job.Service) {
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
		if h.outOfTime(job) {
			h.log.Warn().Str("nzo_id", job.NzoID).Msg("job budget exhausted, not trying further services")
			break
		}
		fbErr := h.tryFallbackService(ctx, job, jobDir, fallbackSvc, fallbackDownload)
		if fbErr == "" {
			return
		}
		lastErr = fbErr
		h.breaker.RecordFailure(fallbackSvc)
		metrics.RecordJobResult(string(sabnzbd.StatusFailed), fallbackSvc)
	}

	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		lastErr = fmt.Sprintf("job wall-clock budget (%s) exhausted: %s", 2*h.cfg.JobTimeout, lastErr)
	}
	h.failJob(job, lastErr)
}

// outOfTime reports whether a job has already spent its whole wall-clock
// budget. Each individual attempt is bounded by SPF_JOB_TIMEOUT, but the
// retry loop multiplies that by maxAttempts and the fallback chain multiplies
// it again - at the 30m default a single hopeless job could hold one of
// SPF_MAX_CONCURRENT slots for hours while Lidarr sat waiting on it. Two
// timeouts is the budget: enough for one slow attempt plus one retry.
func (h *Handler) outOfTime(job *queue.Job) bool {
	if h.cfg.JobTimeout <= 0 || job.TimeAdded.IsZero() {
		return false
	}
	return time.Since(job.TimeAdded) > 2*h.cfg.JobTimeout
}

// runAttemptsWithRetry runs up to `attempts` tries of the download via dl,
// sleeping with backoff and clearing the job dir between them. Returns ""
// on success, the last error otherwise.
type downloadFn func(ctx context.Context, url, outputDir, service, quality string) (<-chan spotiflac.ProgressEvent, <-chan error)

// first and retry may differ: the caller hands the full Python+CLI cascade
// for attempt 1 and a CLI-only backend for retries (see processDownload),
// because the Python cascade's own breaker already proved its providers dead
// for this release by the end of attempt 1.
func (h *Handler) runAttemptsWithRetry(ctx context.Context, job *queue.Job, jobDir string, attempts int, first, retry downloadFn) string {
	var lastErr string
	dl := first
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			dl = retry
		}
		if attempt > 1 && h.outOfTime(job) {
			h.log.Warn().Str("nzo_id", job.NzoID).Int("attempt", attempt).Msg("job budget exhausted, not retrying")
			return lastErr
		}
		if ctx.Err() != nil {
			return "canceled"
		}
		ok, errMsg := h.attemptDownload(ctx, job, jobDir, dl)
		if ok {
			return ""
		}
		lastErr = errMsg
		if attempt < attempts {
			h.log.Warn().Str("nzo_id", job.NzoID).Int("attempt", attempt).Str("error", errMsg).Msg("download attempt failed, retrying")
			if cerr := h.storage.CleanupJob(job.NzoID); cerr != nil {
				h.log.Warn().Err(cerr).Str("nzo_id", job.NzoID).Msg("failed to clean up job dir before retry")
			} else if _, perr := h.storage.PrepareJobDir(job.NzoID); perr != nil {
				h.log.Warn().Err(perr).Str("nzo_id", job.NzoID).Msg("failed to recreate job dir before retry")
			}
			time.Sleep(retryBackoff[attempt-1])
		}
	}
	return lastErr
}

// tryFallbackService switches the job over to svc, resets its job dir and
// runs one download attempt through dl. Returns "" on success (the job has
// been moved to history by attemptDownload), the error otherwise.
func (h *Handler) tryFallbackService(ctx context.Context, job *queue.Job, jobDir, svc string, dl downloadFn) string {
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
// current service, preserving configured order, excluding the current one.
func (h *Handler) fallbackChain(current string) []string {
	var chain []string
	for _, svc := range h.cfg.FallbackServices {
		if svc != current {
			chain = append(chain, svc)
		}
	}
	return chain
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
	// this is only safe when there cannot be a sibling.
	if h.cfg.MaxConcurrent <= 1 {
		reapStaleBrowsers()
	}

	events, errs := dl(ctx, job.SpotifyURL, jobDir, job.Service, job.Quality)

	for {
		select {
		case evt, ok := <-events:
			if !ok {
				// A "complete" event always returns immediately below, so
				// reaching a closed channel here means we never saw one.
				return false, "cli exited without completion signal"
			}
			if evt.Type == "complete" {
				return h.handleCompleteEvent(job, evt)
			}
			h.handleProgressEvent(job, evt)
		case e, ok := <-errs:
			if !ok {
				continue
			}
			if e != nil {
				var de *spotiflac.DownloadError
				if errors.As(e, &de) && de.RawOutput != "" {
					job.CLIOutput = de.RawOutput
					// The backend's own reason lines are the only
					// explanation a failure has. Logging just
					// "spotiflac exited: exit status 1" - which is all
					// this used to emit - leaves nothing to debug with.
					h.log.Warn().
						Str("nzo_id", job.NzoID).
						Str("service", job.Service).
						Str("detail", lastLines(de.RawOutput, 12)).
						Msg("download backend reported a failure")
				}
				return false, e.Error()
			}
		}
	}
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
	onDisk, cerr := storage.CountAudioFiles(evt.OutputPath)
	if cerr != nil {
		return false, fmt.Sprintf("cannot verify %s: %s", evt.OutputPath, cerr)
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
	metrics.RecordJobResult(string(sabnzbd.StatusCompleted), job.Service)
	if !job.TimeAdded.IsZero() {
		metrics.RecordDownloadDuration(job.Service, job.Quality, time.Since(job.TimeAdded).Seconds())
	}
	job.Status = sabnzbd.StatusCompleted
	job.Percentage = 100
	job.Size = evt.Size
	job.Sizeleft = 0
	job.OutputPath = evt.OutputPath
	now := time.Now()
	job.CompletedAt = &now
	job.Filename = releaseName(job.Filename, evt)
	if err := h.queue.Update(job); err != nil {
		h.log.Error().Err(err).Str("nzo_id", job.NzoID).Msg("mark job completed failed")
	}
	if err := h.queue.MoveToHistory(job.NzoID); err != nil {
		h.log.Error().Err(err).Str("nzo_id", job.NzoID).Msg("move job to history failed")
	}
	h.log.Info().Str("nzo_id", job.NzoID).Str("path", evt.OutputPath).Msg("download complete")
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
		Priority:     job.Priority,
		Cat:          job.Category,
		TimeAdded:    job.TimeAdded.Unix(),
		Script:       "Default",
		Unpackopts:   "3",
		AvgAge:       "0d",
		DirectUnpack: "0",
	}
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
