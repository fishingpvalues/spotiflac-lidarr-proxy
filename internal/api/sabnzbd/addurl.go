package sabnzbd

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/config"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/indexer"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/queue"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/pkg/sabnzbd"
)

func (h *Handler) handleAddURL(c fiber.Ctx) error {
	uploaded := uploadedRelease(c)
	spotifyURL := resolveSpotifyURL(c, uploaded)
	if spotifyURL == "" {
		return c.Status(fiber.StatusBadRequest).JSON(sabnzbd.StatusResponse{
			Status: false,
			Error:  "missing 'name' parameter (spotify URL) and no uploaded NZB with an embedded one",
		})
	}
	if !config.IsValidSpotifyURL(spotifyURL) {
		return c.Status(fiber.StatusBadRequest).JSON(sabnzbd.StatusResponse{
			Status: false,
			Error:  "invalid Spotify URL: must be a https://open.spotify.com/(track|album|playlist)/... link",
		})
	}

	nzbName := resolveReleaseName(c, uploaded)
	cat := resolveCategory(c)
	priority := c.Query("priority")
	if priority == "" {
		priority = c.FormValue("priority")
	}
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

	// Size and TrackCount also come from the NZB. Size is the indexer's
	// per-track estimate, not a measurement, but Lidarr renders "0 B" and
	// computes no progress whatsoever from a zero, and the CLI overwrites
	// it with the real byte count on completion. TrackCount is what lets
	// handleCompleteEvent reject a partial album instead of handing Lidarr
	// one file out of thirteen.
	job := &queue.Job{
		NzoID:      nzoID,
		SpotifyURL: spotifyURL,
		Category:   cat,
		Priority:   priority,
		Filename:   nzbName,
		Service:    svc,
		Quality:    qual,
		Size:       uploaded.Size,
		Sizeleft:   uploaded.Size,
		TrackCount: uploaded.TrackCount,
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

// resolveReleaseName decides what the job is called.
//
// Lidarr's real grab path is mode=addfile, and its POST carries no nzbname at
// all - verified against production, where the whole query was
// mode=addfile&cat=music&priority=-100&apikey=...&output=json. The release
// name therefore has to come out of the NZB we generated ourselves (t=get
// folds it in), with the uploaded part's own filename as a last resort.
// Leaving it empty marshals the queue slot with `"filename": ""`, and Lidarr
// keys its tracked download off that string: the download ran to completion
// and Lidarr's queue stayed empty, so it never imported and re-grabbed the
// same album forever.
func resolveReleaseName(c fiber.Ctx, uploaded uploadedNZB) string {
	for _, candidate := range []string{
		c.Query("nzbname"),
		c.FormValue("nzbname"),
		uploaded.Name,
		uploaded.UploadFilename,
	} {
		if candidate != "" {
			return candidate
		}
	}
	return ""
}

func resolveCategory(c fiber.Ctx) string {
	// Query first (what Lidarr's Sabnzbd client sends), then the multipart
	// form - real SABnzbd accepts both, and a caller that posts cat as a
	// form field must not silently fall back to the default category.
	cat := c.Query("cat")
	if cat == "" {
		cat = c.FormValue("cat")
	}
	if cat == "" || cat == "*" {
		return "music-flac-16"
	}
	return cat
}

// uploadedNZB is what a mode=addfile upload yielded: the release metadata
// our own synthetic NZB embedded, plus the multipart part's filename as a
// fallback name. Zero value means there was no usable upload.
type uploadedNZB struct {
	indexer.Release
	UploadFilename string
}

// resolveSpotifyURL covers both SABnzbd add modes: mode=addurl passes the
// Spotify URL directly as "name"; mode=addfile (what Lidarr's real grab
// flow uses) uploads our synthetic NZB's bytes instead, so the URL has to
// be recovered from its embedded metadata.
func resolveSpotifyURL(c fiber.Ctx, uploaded uploadedNZB) string {
	if name := c.Query("name"); name != "" {
		return name
	}
	if name := c.FormValue("name"); name != "" {
		return name
	}
	return uploaded.SpotifyURL
}

// uploadedRelease looks for an uploaded file in a mode=addfile request
// (SABnzbd clients vary in which multipart field name they use for the .nzb,
// so this checks all of them) and parses back the metadata our own synthetic
// NZB embeds (see indexer.GenerateNZBRelease).
func uploadedRelease(c fiber.Ctx) uploadedNZB {
	form, err := c.Req().MultipartForm()
	if err != nil {
		return uploadedNZB{}
	}
	for _, files := range form.File {
		for _, fh := range files {
			f, err := fh.Open()
			if err != nil {
				continue
			}
			data, err := io.ReadAll(f)
			f.Close()
			if err != nil {
				continue
			}
			release, err := indexer.ParseNZBMeta(data)
			if err != nil {
				continue
			}
			return uploadedNZB{
				Release:        release,
				UploadFilename: strings.TrimSuffix(fh.Filename, ".nzb"),
			}
		}
	}
	return uploadedNZB{}
}
