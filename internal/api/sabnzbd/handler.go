package sabnzbd

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

const maxAttempts = 3

var retryBackoff = []time.Duration{5 * time.Second, 15 * time.Second}

func (h *Handler) processDownload(job *queue.Job) {
	h.sem <- struct{}{}
	defer func() { <-h.sem }()

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
		lastErr = h.runAttemptsWithRetry(job, jobDir, maxAttempts)
		if lastErr == "" {
			return
		}
		h.breaker.RecordFailure(primarySvc)
		metrics.RecordJobResult(string(sabnzbd.StatusFailed), primarySvc)
	}

	for _, fallbackSvc := range h.fallbackChain(job.Service) {
		if !h.breaker.Allow(fallbackSvc) {
			continue
		}
		h.log.Warn().Str("nzo_id", job.NzoID).Str("from_service", job.Service).Str("to_service", fallbackSvc).Msg("falling back to next service")
		job.Service = fallbackSvc
		if err := h.queue.Update(job); err != nil {
			h.log.Error().Err(err).Str("nzo_id", job.NzoID).Msg("record fallback service failed")
		}
		if cerr := h.storage.CleanupJob(job.NzoID); cerr != nil {
			h.log.Warn().Err(cerr).Str("nzo_id", job.NzoID).Msg("failed to clean up job dir before fallback attempt")
		} else if _, perr := h.storage.PrepareJobDir(job.NzoID); perr != nil {
			h.log.Warn().Err(perr).Str("nzo_id", job.NzoID).Msg("failed to recreate job dir before fallback attempt")
		}
		if fbErr := h.runAttemptsWithRetry(job, jobDir, 1); fbErr == "" {
			return
		} else {
			lastErr = fbErr
			h.breaker.RecordFailure(fallbackSvc)
			metrics.RecordJobResult(string(sabnzbd.StatusFailed), fallbackSvc)
		}
	}

	h.failJob(job, lastErr)
}

// runAttemptsWithRetry runs up to `attempts` tries of the download, sleeping
// with backoff and clearing the job dir between them. Returns "" on success,
// the last error otherwise.
func (h *Handler) runAttemptsWithRetry(job *queue.Job, jobDir string, attempts int) string {
	var lastErr string
	for attempt := 1; attempt <= attempts; attempt++ {
		ok, errMsg := h.attemptDownload(job, jobDir)
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

// attemptDownload runs a single CLI invocation and reports whether it
// succeeded. On success it fully updates the job to Completed and moves it
// to history itself (mirroring the previous inline behavior); on failure it
// returns false with the error message and leaves the job untouched for the
// caller to retry or ultimately fail.
func (h *Handler) attemptDownload(job *queue.Job, jobDir string) (bool, string) {
	ctx := context.Background()
	events, errs := h.client.Download(ctx, job.SpotifyURL, jobDir, job.Service, job.Quality)

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
		job.Percentage = evt.Percent
		job.Sizeleft = int64(float64(job.Size) * (100 - evt.Percent) / 100)
		if err := h.queue.Update(job); err != nil {
			h.log.Error().Err(err).Str("nzo_id", job.NzoID).Msg("progress update failed")
		}
	case "metadata":
		job.Filename = evt.Artist + " - " + evt.Album
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
	if job.TrackCount > 0 {
		gotCount, cerr := storage.CountAudioFiles(evt.OutputPath)
		if cerr != nil || gotCount < job.TrackCount {
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
	job.Filename = evt.Artist + " - " + evt.Album
	if err := h.queue.Update(job); err != nil {
		h.log.Error().Err(err).Str("nzo_id", job.NzoID).Msg("mark job completed failed")
	}
	if err := h.queue.MoveToHistory(job.NzoID); err != nil {
		h.log.Error().Err(err).Str("nzo_id", job.NzoID).Msg("move job to history failed")
	}
	h.log.Info().Str("nzo_id", job.NzoID).Str("path", evt.OutputPath).Msg("download complete")
	return true, ""
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
