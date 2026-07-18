package sabnzbd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/rs/zerolog"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/breaker"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/config"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/queue"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/storage"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/pkg/sabnzbd"
)

const maxConcurrent = 3

type Handler struct {
	queue   *queue.SQLiteQueue
	client  *spotiflac.Client
	storage *storage.Storage
	cfg     *config.Config
	version string
	log     zerolog.Logger
	sem     chan struct{}
	breaker *breaker.Breaker
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

	switch {
	case mode == "version":
		return h.handleVersion(c)
	case mode == "auth":
		return h.handleAuth(c)
	case mode == "get_config":
		return h.handleGetConfig(c)
	case mode == "get_cats":
		return h.handleGetCats(c)
	case mode == "fullstatus":
		return h.handleFullStatus(c)
	case mode == "addurl" || mode == "addfile":
		return h.handleAddURL(c)
	case mode == "queue":
		name := c.Query("name")
		switch name {
		case "pause":
			return h.handlePause(c)
		case "resume":
			return h.handleResume(c)
		case "delete":
			return h.handleDelete(c)
		default:
			return h.handleQueue(c)
		}
	case mode == "history":
		return h.handleHistory(c)
	case mode == "change_cat":
		return h.handleChangeCat(c)
	case mode == "server_stats":
		return h.handleServerStats(c)
	case mode == "status":
		return h.handleStatus(c)
	case mode == "retry":
		return h.handleRetry(c)
	case mode == "warnings":
		return h.handleWarnings(c)
	case mode == "pause_all":
		return h.handlePauseAll(c)
	case mode == "resume_all":
		return h.handleResumeAll(c)
	case mode == "set_speedlimit":
		return h.handleSetSpeedlimit(c)
	default:
		return c.Status(fiber.StatusBadRequest).JSON(sabnzbd.StatusResponse{
			Status: false,
			Error:  fmt.Sprintf("unknown mode: %s", mode),
		})
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

func (h *Handler) processDownload(job *queue.Job) {
	h.sem <- struct{}{}
	defer func() { <-h.sem }()

	if !h.breaker.Allow(job.Service) {
		h.failJob(job, fmt.Sprintf("service %s temporarily unavailable (circuit open)", job.Service))
		return
	}

	jobDir, err := h.storage.PrepareJobDir(job.NzoID)
	if err != nil {
		h.failJob(job, err.Error())
		return
	}

	job.Status = sabnzbd.StatusDownloading
	job.OutputPath = jobDir
	h.queue.Update(job)

	ctx := context.Background()
	events, errs := h.client.Download(ctx, job.SpotifyURL, jobDir, job.Service, job.Quality)

	sawComplete := false

	for {
		select {
		case evt, ok := <-events:
			if !ok {
				if !sawComplete {
					h.failJob(job, "cli exited without completion signal")
				}
				return
			}
			if evt.Type == "progress" {
				job.Percentage = evt.Percent
				job.Sizeleft = int64(float64(job.Size) * (100 - evt.Percent) / 100)
				h.queue.Update(job)
			}
			if evt.Type == "complete" {
				sawComplete = true
				if job.TrackCount > 0 {
					gotCount, cerr := storage.CountAudioFiles(evt.OutputPath)
					if cerr != nil || gotCount < job.TrackCount {
						h.failJob(job, fmt.Sprintf("partial album: %d/%d tracks", gotCount, job.TrackCount))
						return
					}
				}
				h.breaker.RecordSuccess(job.Service)
				job.Status = sabnzbd.StatusCompleted
				job.Percentage = 100
				job.Size = evt.Size
				job.Sizeleft = 0
				job.OutputPath = evt.OutputPath
				now := time.Now()
				job.CompletedAt = &now
				job.Filename = evt.Artist + " - " + evt.Album
				h.queue.Update(job)
				h.queue.MoveToHistory(job.NzoID)
				h.log.Info().Str("nzo_id", job.NzoID).Str("path", evt.OutputPath).Msg("download complete")
				return
			}
			if evt.Type == "metadata" {
				job.Filename = evt.Artist + " - " + evt.Album
				h.queue.Update(job)
			}
		case e, ok := <-errs:
			if !ok {
				return
			}
			if e != nil {
				h.failJob(job, e.Error())
				return
			}
		}
	}
}

func (h *Handler) failJob(job *queue.Job, errMsg string) {
	job.Status = sabnzbd.StatusFailed
	job.ErrorMessage = errMsg
	now := time.Now()
	job.CompletedAt = &now
	h.queue.Update(job)
	h.queue.MoveToHistory(job.NzoID)
	h.breaker.RecordFailure(job.Service)
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
