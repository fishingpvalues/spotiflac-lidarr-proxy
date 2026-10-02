package sabnzbd

import (
	"fmt"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/queue"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/pkg/sabnzbd"
)

func (h *Handler) handleVersion(c fiber.Ctx) error {
	return c.JSON(sabnzbd.VersionResponse{Version: sabnzbdVersion(h.version)})
}

func (h *Handler) handleAuth(c fiber.Ctx) error {
	return c.JSON(sabnzbd.AuthResponse{Auth: true})
}

func (h *Handler) handleGetConfig(c fiber.Ctx) error {
	resp := sabnzbd.ConfigResponse{}
	// Dir is deliberately empty on every category. Downloads land directly in
	// OutputDir/<nzo_id>, never in a per-category subdirectory, and Lidarr
	// resolves a relative category dir against complete_dir to decide where the
	// client writes: a "music" dir made it check OutputDir/music, which never
	// exists, and it then raised a permanent
	// "download client SpotiFLAC places downloads in ... but this directory does
	// not appear to exist inside the container" health error against a working
	// setup. Category names still route service and quality (ParseCategory).
	resp.Config.Categories = []sabnzbd.Category{
		// Generic quality categories
		{Name: "music", Order: 0},
		{Name: "music-flac-16", Order: 1},
		{Name: "music-flac-24", Order: 2},
		{Name: "music-lossless", Order: 3},
		{Name: "music-mp3", Order: 4},
		// Service-specific categories
		{Name: "music-tidal", Order: 10},
		{Name: "music-qobuz", Order: 11},
		{Name: "music-amazon", Order: 12},
		{Name: "music-deezer", Order: 13},
		// Service x Quality combined categories
		{Name: "music-tidal-flac-16", Order: 20},
		{Name: "music-tidal-flac-24", Order: 21},
		{Name: "music-qobuz-flac-16", Order: 22},
		{Name: "music-qobuz-flac-24", Order: 23},
		{Name: "music-amazon-flac-16", Order: 24},
		{Name: "music-amazon-flac-24", Order: 25},
		{Name: "music-deezer-flac-16", Order: 26},
		{Name: "music-deezer-flac-24", Order: 27},
	}
	resp.Config.Scripts = []sabnzbd.Script{
		{Name: "Default", Default: true},
	}
	resp.Config.Speedlimit = "100"
	resp.Config.Misc.Version = h.version
	resp.Config.Misc.CompletedDir = h.cfg.OutputDir
	resp.Config.Misc.CompleteDirEnabled = true
	resp.Config.Misc.PreCheck = false
	// We never prune history behind Lidarr's back (PruneHistory only trims
	// our own oldest entries, well past import), so both retention fields
	// must say so. history_retention_option is the one that matters on
	// SABnzbd >= 4.3 and is what Lidarr checks first; without it Lidarr
	// falls through to its legacy branch, which ends in
	//
	//   return retention != "0";
	//
	// and so read our "all" - meaning "keep everything" - as "removes
	// completed downloads", raising
	// DownloadClientRemovesCompletedDownloadsCheck against a client that
	// removes nothing.
	resp.Config.Misc.HistoryRetention = "all"
	resp.Config.Misc.HistoryRetentionOption = "all"
	return c.JSON(resp)
}

func (h *Handler) handleFullStatus(c fiber.Ctx) error {
	return c.JSON(sabnzbd.FullStatusResponse{
		Status: sabnzbd.FullStatus{CompleteDir: h.cfg.OutputDir},
	})
}

func (h *Handler) handleGetCats(c fiber.Ctx) error {
	return c.JSON(sabnzbd.CategoriesResponse{
		Categories: []string{
			"music", "music-flac-16", "music-flac-24", "music-lossless", "music-mp3",
			"music-tidal", "music-qobuz", "music-amazon", "music-deezer",
			"music-tidal-flac-16", "music-tidal-flac-24",
			"music-qobuz-flac-16", "music-qobuz-flac-24",
			"music-amazon-flac-16", "music-amazon-flac-24",
			"music-deezer-flac-16", "music-deezer-flac-24",
		},
	})
}

func (h *Handler) handleStatus(c fiber.Ctx) error {
	return c.JSON(sabnzbd.SimpleStatusResponse{
		Paused: false,
	})
}

func (h *Handler) handleRetry(c fiber.Ctx) error {
	nzoID := c.Query("value")
	if nzoID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(sabnzbd.StatusResponse{
			Status: false, Error: "missing nzo_id",
		})
	}

	// Move job back from history to active queue. GetByNzoID, not Get: Get
	// filters `is_history = 0`, so this endpoint - which exists to retry a
	// FAILED download and is documented as such - could only ever answer
	// 404 for the one kind of job it is for.
	job, err := h.queue.GetByNzoID(nzoID)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(sabnzbd.StatusResponse{
			Status: false, Error: "job not found",
		})
	}

	// Cancel whatever is still running for this id before re-dispatching:
	// otherwise a retry of a live job starts a second worker on the same row
	// and the same output directory.
	h.CancelJob(nzoID)
	job.Status = sabnzbd.StatusQueued
	job.ErrorMessage = ""
	job.CompletedAt = nil
	job.Percentage = 0
	job.StartedAt = nil
	job.ProgressAt = nil
	if err := h.queue.MoveToQueue(nzoID); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(sabnzbd.StatusResponse{
			Status: false, Error: err.Error(),
		})
	}
	if err := h.queue.Update(job); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(sabnzbd.StatusResponse{
			Status: false, Error: err.Error(),
		})
	}

	// Restart the download
	h.clearRequeues(nzoID)
	h.dispatchJob(job)

	return c.JSON(sabnzbd.RetryResponse{
		Status: true,
		NzoID:  nzoID,
	})
}

func (h *Handler) handleWarnings(c fiber.Ctx) error {
	return c.JSON(sabnzbd.WarningsResponse{Warnings: h.Warnings()})
}

// Warnings builds the operator-facing warning list. Split from the HTTP
// handler so tests can assert on the content without a request.
func (h *Handler) Warnings() []sabnzbd.Warning {
	var warnings []sabnzbd.Warning
	if text := h.breakGate.summarize(); text != "" {
		warnings = append(warnings, sabnzbd.Warning{
			Time: time.Now().Unix(),
			Type: "WARNING",
			Text: text,
			ID:   "upstream_break",
		})
	}
	for service, state := range h.breaker.Status() {
		if !state.Open {
			continue
		}
		warnings = append(warnings, sabnzbd.Warning{
			Time: state.OpenedAt.Unix(),
			Type: "ERROR",
			Text: fmt.Sprintf("service %s circuit open, retrying after %s", service, state.RetryAt.Format(time.RFC3339)),
			ID:   "breaker_" + service,
		})
	}

	// The circuit park. Without this entry the proxy is indistinguishable
	// from a hang while it waits out a provider cooldown: slots in the
	// queue, 0 B/s, and deliberately no outbound requests. The external
	// stuck-monitor on potatostack watched exactly those two numbers and
	// restarted this container eight times in the week to 2026-10-02, each
	// time throwing away the breakers, the park and every in-flight job.
	if remaining, services := h.circuitParkState(); remaining > 0 {
		warnings = append(warnings, sabnzbd.Warning{
			Time: time.Now().Unix(),
			Type: "WARNING",
			Text: fmt.Sprintf("circuit park active: queue held for %s while %v cool down",
				remaining.Round(time.Second), services),
			ID: "circuit_park",
		})
	}

	if h.verifyStore != nil {
		if link, at, pending := h.verifyStore.Pending(); pending {
			warnings = append(warnings, sabnzbd.Warning{
				Time: at.Unix(),
				Type: "WARNING",
				Text: fmt.Sprintf("Tidal/Qobuz/Amazon one-time verification needed, open this URL in a browser to continue: %s", link),
				ID:   "verification_required",
			})
		}
	}

	if h.backendWarnings != nil {
		for _, w := range h.backendWarnings() {
			warnings = append(warnings, sabnzbd.Warning{
				Time: time.Now().Unix(),
				Type: "WARNING",
				Text: w.Text,
				ID:   w.ID,
			})
		}
	}

	stuck, _, err := h.queue.List(queue.ListParams{Status: string(sabnzbd.StatusDownloading), Limit: 1000})
	if err == nil {
		for _, job := range stuck {
			// Measure the RUN, not the wait. TimeAdded is when the job
			// entered the queue, which with a backlog is hours before it
			// ever took a slot: measured 2026-08-27, ~50 queued jobs were
			// all reported as "downloading for longer than 2x the timeout"
			// the moment the check looked at them. StartedAt is written by
			// beginDownload; a job that predates that column falls back to
			// TimeAdded.
			since := job.TimeAdded
			if job.StartedAt != nil {
				since = *job.StartedAt
			}
			if age := time.Since(since); age > 2*h.cfg.JobTimeout {
				warnings = append(warnings, sabnzbd.Warning{
					Time: since.Unix(),
					Type: "WARNING",
					Text: fmt.Sprintf("job %s (%s) has been downloading for %s, more than 2x the configured timeout", job.NzoID, job.Filename, age.Round(time.Second)),
					ID:   "stuck_" + job.NzoID,
				})
			}
		}
	}

	if warnings == nil {
		warnings = []sabnzbd.Warning{}
	}
	return warnings
}
