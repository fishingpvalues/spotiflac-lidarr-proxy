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
		// Generic quality categories
		{Name: "music", Order: 0, Dir: "music"},
		{Name: "music-flac-16", Order: 1, Dir: "music-flac-16"},
		{Name: "music-flac-24", Order: 2, Dir: "music-flac-24"},
		{Name: "music-lossless", Order: 3, Dir: "music-lossless"},
		{Name: "music-mp3", Order: 4, Dir: "music-mp3"},
		// Service-specific categories
		{Name: "music-tidal", Order: 10, Dir: "music-tidal"},
		{Name: "music-qobuz", Order: 11, Dir: "music-qobuz"},
		{Name: "music-amazon", Order: 12, Dir: "music-amazon"},
		{Name: "music-deezer", Order: 13, Dir: "music-deezer"},
		// Service x Quality combined categories
		{Name: "music-tidal-flac-16", Order: 20, Dir: "music-tidal-flac-16"},
		{Name: "music-tidal-flac-24", Order: 21, Dir: "music-tidal-flac-24"},
		{Name: "music-qobuz-flac-16", Order: 22, Dir: "music-qobuz-flac-16"},
		{Name: "music-qobuz-flac-24", Order: 23, Dir: "music-qobuz-flac-24"},
		{Name: "music-amazon-flac-16", Order: 24, Dir: "music-amazon-flac-16"},
		{Name: "music-amazon-flac-24", Order: 25, Dir: "music-amazon-flac-24"},
		{Name: "music-deezer-flac-16", Order: 26, Dir: "music-deezer-flac-16"},
		{Name: "music-deezer-flac-24", Order: 27, Dir: "music-deezer-flac-24"},
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

	// Move job back from history to active queue
	job, err := h.queue.Get(nzoID)
	if err != nil {
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
