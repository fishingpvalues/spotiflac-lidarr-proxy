package sabnzbd

import (
	"github.com/gofiber/fiber/v3"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/queue"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/pkg/sabnzbd"
)

// listAllActive pages the whole active queue.
//
// queue.List defaults Limit to 50, so the single call these handlers used to
// make silently stopped at the fiftieth job: pause_all left jobs 51+ running
// and resume_all never resumed them, reporting success either way.
func (h *Handler) listAllActive() ([]*queue.Job, error) {
	const page = 200
	var all []*queue.Job
	for start := 0; ; start += page {
		jobs, total, err := h.queue.List(queue.ListParams{Start: start, Limit: page})
		if err != nil {
			return nil, err
		}
		all = append(all, jobs...)
		if len(jobs) < page || start+page >= total {
			return all, nil
		}
	}
}

// handlePauseAll pauses the queue.
//
// It also CANCELS each in-flight download, which is what pausing means: the
// status alone did not stop anything, so the running worker kept downloading
// and its next progress event wrote status=Downloading straight back over the
// pause - and resume_all then started a SECOND worker for the same job.
func (h *Handler) handlePauseAll(c fiber.Ctx) error {
	jobs, err := h.listAllActive()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(sabnzbd.StatusResponse{
			Status: false, Error: err.Error(),
		})
	}
	for _, job := range jobs {
		if job.Status != sabnzbd.StatusDownloading && job.Status != sabnzbd.StatusQueued {
			continue
		}
		if job.Status == sabnzbd.StatusDownloading {
			h.CancelJob(job.NzoID)
		}
		job.Status = sabnzbd.StatusPaused
		if err := h.queue.Update(job); err != nil {
			h.log.Error().Err(err).Str("nzo_id", job.NzoID).Msg("pause_all: update job failed")
		}
	}
	return c.JSON(sabnzbd.StatusResponse{Status: true})
}

func (h *Handler) handleResumeAll(c fiber.Ctx) error {
	jobs, err := h.listAllActive()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(sabnzbd.StatusResponse{
			Status: false, Error: err.Error(),
		})
	}
	for _, job := range jobs {
		if job.Status != sabnzbd.StatusPaused {
			continue
		}
		job.Status = sabnzbd.StatusQueued
		job.CompletedAt = nil
		job.StartedAt = nil
		if err := h.queue.Update(job); err != nil {
			h.log.Error().Err(err).Str("nzo_id", job.NzoID).Msg("resume_all: update job failed")
			continue
		}
		h.dispatchJob(job)
	}
	return c.JSON(sabnzbd.StatusResponse{Status: true})
}

func (h *Handler) handleSetSpeedlimit(c fiber.Ctx) error {
	// Speed limit not applicable to SpotiFLAC, just acknowledge
	return c.JSON(sabnzbd.StatusResponse{Status: true})
}
