package sabnzbd

import (
	"database/sql"
	"errors"
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
	if !config.IsValidSpotifyURL(spotifyURL) {
		return c.Status(fiber.StatusBadRequest).JSON(sabnzbd.StatusResponse{
			Status: false,
			Error:  "invalid Spotify URL: must be a https://open.spotify.com/(track|album|playlist)/... link",
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

	existing, err := h.queue.FindActiveBySpotifyURL(spotifyURL)
	if err == nil {
		return c.JSON(sabnzbd.AddURLResponse{
			Status: true,
			NzoIDs: []string{existing.NzoID},
		})
	}
	if !errors.Is(err, sql.ErrNoRows) {
		h.log.Warn().Err(err).Str("spotify_url", spotifyURL).Msg("dedup lookup failed, proceeding to create new job")
	}

	if h.cfg.HistoryRetentionCount > 0 {
		if err := h.queue.PruneHistory(h.cfg.HistoryRetentionCount); err != nil {
			h.log.Warn().Err(err).Msg("history prune failed")
		}
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
		// TrackCount left at 0: the CLI's --search flag takes free-text
		// queries, not a Spotify URL, so no reliable per-URL track count
		// is available at addurl time. Completion verification in
		// processDownload only runs when TrackCount > 0.
	}

	if err := h.queue.Add(job); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(sabnzbd.StatusResponse{
			Status: false,
			Error:  fmt.Sprintf("queue add: %s", err),
		})
	}

	go h.ProcessDownloadSync(job)

	return c.JSON(sabnzbd.AddURLResponse{
		Status: true,
		NzoIDs: []string{nzoID},
	})
}
