package sabnzbd

import (
	"fmt"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/config"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/queue"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/pkg/sabnzbd"
)

func (h *Handler) handleAddURL(c fiber.Ctx) error {
	spotifyURL := c.Query("name")
	if spotifyURL == "" {
		spotifyURL = c.FormValue("name")
	}
	if spotifyURL == "" {
		return c.Status(fiber.StatusBadRequest).JSON(sabnzbd.StatusResponse{
			Status: false,
			Error:  "missing 'name' parameter (spotify URL)",
		})
	}

	nzbName := c.Query("nzbname")
	if nzbName == "" {
		nzbName = c.FormValue("nzbname")
	}
	cat := c.Query("cat")
	if cat == "" || cat == "*" {
		cat = "music-flac-16"
	}
	priority := c.Query("priority")
	if priority == "" {
		priority = "Normal"
	}

	nzoID := "SABnzbd_nzo_" + uuid.New().String()[:12]

	// Extract service and quality from category
	svc, qual := config.ParseCategory(cat)
	if svc == "" {
		svc = h.cfg.DefaultService
	}
	if qual == "" {
		qual = h.cfg.DefaultQuality
	}

	job := &queue.Job{
		NzoID:      nzoID,
		SpotifyURL: spotifyURL,
		Category:   cat,
		Priority:   priority,
		Filename:   nzbName,
		Service:    svc,
		Quality:    qual,
	}

	if err := h.queue.Add(job); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(sabnzbd.StatusResponse{
			Status: false,
			Error:  fmt.Sprintf("queue add: %s", err),
		})
	}

	go h.processDownload(job)

	return c.JSON(sabnzbd.AddURLResponse{
		Status: true,
		NzoIDs: []string{nzoID},
	})
}
