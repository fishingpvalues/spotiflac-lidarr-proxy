package sabnzbd

import (
	"github.com/gofiber/fiber/v3"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/queue"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/pkg/sabnzbd"
)

func (h *Handler) handlePauseAll(c fiber.Ctx) error {
	jobs, _, err := h.queue.List(queue.ListParams{})
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(sabnzbd.StatusResponse{
			Status: false, Error: err.Error(),
		})
	}
	for _, job := range jobs {
		if job.Status == sabnzbd.StatusDownloading || job.Status == sabnzbd.StatusQueued {
			job.Status = sabnzbd.StatusPaused
			h.queue.Update(job)
		}
	}
	return c.JSON(sabnzbd.StatusResponse{Status: true})
}

func (h *Handler) handleResumeAll(c fiber.Ctx) error {
	jobs, _, err := h.queue.List(queue.ListParams{})
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(sabnzbd.StatusResponse{
			Status: false, Error: err.Error(),
		})
	}
	for _, job := range jobs {
		if job.Status == sabnzbd.StatusPaused {
			job.Status = sabnzbd.StatusQueued
			h.queue.Update(job)
			go h.processDownload(job)
		}
	}
	return c.JSON(sabnzbd.StatusResponse{Status: true})
}

func (h *Handler) handleSetSpeedlimit(c fiber.Ctx) error {
	// Speed limit not applicable to SpotiFLAC, just acknowledge
	return c.JSON(sabnzbd.StatusResponse{Status: true})
}
