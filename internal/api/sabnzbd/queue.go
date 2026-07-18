package sabnzbd

import (
	"strconv"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/queue"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/pkg/sabnzbd"
)

func (h *Handler) handleQueue(c fiber.Ctx) error {
	start, _ := strconv.Atoi(c.Query("start", "0"))
	limit, _ := strconv.Atoi(c.Query("limit", "50"))

	params := queue.ListParams{
		Start:    start,
		Limit:    limit,
		Search:   c.Query("search", ""),
		Category: c.Query("cat", ""),
		Status:   c.Query("status", ""),
	}

	nzoIDs := c.Query("nzo_ids", "")
	if nzoIDs != "" {
		params.NzoIDs = splitComma(nzoIDs)
	}

	jobs, total, err := h.queue.List(params)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(sabnzbd.StatusResponse{
			Status: false,
			Error:  err.Error(),
		})
	}

	resp := sabnzbd.QueueResponse{}
	resp.Queue.Status = "Idle"
	resp.Queue.Speedlimit = "100"
	resp.Queue.SpeedlimitAbs = "0"
	resp.Queue.Noofslots = len(jobs)
	resp.Queue.NoofslotsTotal = total
	resp.Queue.Limit = limit
	resp.Queue.Start = start
	resp.Queue.Version = h.version
	resp.Queue.PausedAll = false

	var totalMb, totalMbleft float64
	var hasDownloading bool
	var totalSpeed float64

	for i, job := range jobs {
		slot := jobToSlot(job, i)
		totalMb += float64(job.Size) / (1024 * 1024)
		totalMbleft += float64(job.Sizeleft) / (1024 * 1024)

		if job.Status == sabnzbd.StatusDownloading {
			hasDownloading = true
			// Estimate speed: if size changed recently, calculate from bytes/sec
			// Otherwise use default estimate
			if job.Size > 0 && job.Sizeleft > 0 && job.Size > job.Sizeleft {
				downloaded := float64(job.Size - job.Sizeleft)
				// Assume 10 seconds avg since last update for speed calc
				estimatedSpeed := downloaded / 10.0
				totalSpeed += estimatedSpeed
			}
		}

		resp.Queue.Slots = append(resp.Queue.Slots, slot)
	}

	if hasDownloading {
		resp.Queue.Status = "Downloading"
	}

	resp.Queue.Mb = totalMb
	resp.Queue.Mbleft = totalMbleft

	// Calculate total timeleft based on total size left and speed
	if totalSpeed > 0 {
		secs := int(totalMbleft * 1024 * 1024 / totalSpeed)
		resp.Queue.Timeleft = formatDuration(secs)
		resp.Queue.Finish = int(time.Now().Unix()) + secs

		// Format speed for display
		if totalSpeed >= 1024*1024 {
			resp.Queue.Speed = formatBytes(int64(totalSpeed))
			resp.Queue.Kbpersec = strconv.FormatFloat(totalSpeed/1024, 'f', 1, 64)
		} else if totalSpeed >= 1024 {
			resp.Queue.Speed = formatBytes(int64(totalSpeed))
			resp.Queue.Kbpersec = strconv.FormatFloat(totalSpeed/1024, 'f', 1, 64)
		} else {
			resp.Queue.Speed = "0 B"
			resp.Queue.Kbpersec = "0.0"
		}
	} else {
		resp.Queue.Timeleft = "0:00:00"
		resp.Queue.Speed = "0 B"
		resp.Queue.Kbpersec = "0.0"
	}

	free1, total1, err := h.storage.GetDiskSpace()
	if err != nil {
		h.log.Warn().Err(err).Msg("failed to get disk space")
	} else {
		resp.Queue.Diskspace1 = free1
		resp.Queue.Diskspacetotal1 = total1
		resp.Queue.Diskspace2 = free1
		resp.Queue.Diskspacetotal2 = total1
	}

	return c.JSON(resp)
}

func formatDuration(secs int) string {
	if secs <= 0 {
		return "0:00:00"
	}
	h := secs / 3600
	m := (secs % 3600) / 60
	s := secs % 60
	return strconv.Itoa(h) + ":" + pad2(m) + ":" + pad2(s)
}

func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

func (h *Handler) handlePause(c fiber.Ctx) error {
	nzoID := c.Query("value")
	if nzoID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(sabnzbd.StatusResponse{
			Status: false, Error: "missing nzo_id",
		})
	}
	job, err := h.queue.Get(nzoID)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(sabnzbd.StatusResponse{
			Status: false, Error: "job not found",
		})
	}
	job.Status = sabnzbd.StatusPaused
	if err := h.queue.Update(job); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(sabnzbd.StatusResponse{
			Status: false, Error: err.Error(),
		})
	}
	return c.JSON(sabnzbd.StatusResponse{Status: true, NzoIDs: []string{nzoID}})
}

func (h *Handler) handleResume(c fiber.Ctx) error {
	nzoID := c.Query("value")
	if nzoID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(sabnzbd.StatusResponse{
			Status: false, Error: "missing nzo_id",
		})
	}
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
	go h.processDownload(job)
	return c.JSON(sabnzbd.StatusResponse{Status: true, NzoIDs: []string{nzoID}})
}

func (h *Handler) handleDelete(c fiber.Ctx) error {
	nzoID := c.Query("value")
	if nzoID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(sabnzbd.StatusResponse{
			Status: false, Error: "missing nzo_id",
		})
	}
	delFiles := c.Query("del_files") == "1"
	if err := h.queue.Delete(nzoID, delFiles); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(sabnzbd.StatusResponse{
			Status: false, Error: err.Error(),
		})
	}
	if delFiles {
		if err := h.storage.CleanupJob(nzoID); err != nil {
			h.log.Warn().Err(err).Str("nzo_id", nzoID).Msg("failed to cleanup job files")
		}
	}
	return c.JSON(sabnzbd.StatusResponse{Status: true, NzoIDs: []string{nzoID}})
}
