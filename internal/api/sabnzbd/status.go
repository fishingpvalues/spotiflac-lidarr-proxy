package sabnzbd

import (
	"github.com/gofiber/fiber/v3"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/pkg/sabnzbd"
)

func (h *Handler) handleVersion(c fiber.Ctx) error {
	return c.JSON(sabnzbd.VersionResponse{Version: h.version})
}

func (h *Handler) handleAuth(c fiber.Ctx) error {
	return c.JSON(sabnzbd.AuthResponse{Auth: true})
}

func (h *Handler) handleGetConfig(c fiber.Ctx) error {
	resp := sabnzbd.ConfigResponse{}
	resp.Config.Categories = []sabnzbd.Category{
		{Name: "music", Order: 0, Dir: "music"},
		{Name: "music-flac-16", Order: 0, Dir: "music-flac-16"},
		{Name: "music-flac-24", Order: 1, Dir: "music-flac-24"},
		{Name: "music-mp3", Order: 2, Dir: "music-mp3"},
	}
	resp.Config.Scripts = []sabnzbd.Script{
		{Name: "Default", Default: true},
	}
	resp.Config.Speedlimit = "100"
	resp.Config.Misc.Version = h.version
	resp.Config.Misc.CompletedDir = h.cfg.OutputDir
	resp.Config.Misc.CompleteDirEnabled = true
	resp.Config.Misc.PreCheck = false
	resp.Config.Misc.HistoryRetention = "all"
	return c.JSON(resp)
}

func (h *Handler) handleFullStatus(c fiber.Ctx) error {
	return c.JSON(sabnzbd.FullStatusResponse{
		CompleteDir: h.cfg.OutputDir,
	})
}

func (h *Handler) handleGetCats(c fiber.Ctx) error {
	return c.JSON(sabnzbd.CategoriesResponse{
		Categories: []string{"music", "music-flac-16", "music-flac-24", "music-mp3"},
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

	// Move job back from history to active queue
	job, err := h.queue.Get(nzoID)
	if err != nil {
		// Job might be in history - retrieve it differently
		return c.Status(fiber.StatusNotFound).JSON(sabnzbd.StatusResponse{
			Status: false, Error: "job not found",
		})
	}

	job.Status = sabnzbd.StatusQueued
	if err := h.queue.Update(job); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(sabnzbd.StatusResponse{
			Status: false, Error: err.Error(),
		})
	}

	// Restart the download
	go h.processDownload(job)

	return c.JSON(sabnzbd.RetryResponse{
		Status: true,
		NzoID:  nzoID,
	})
}

func (h *Handler) handleWarnings(c fiber.Ctx) error {
	return c.JSON(sabnzbd.WarningsResponse{
		Warnings: []sabnzbd.Warning{},
	})
}
