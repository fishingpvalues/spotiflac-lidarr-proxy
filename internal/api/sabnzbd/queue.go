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
		Category: firstQuery(c, "cat", "category"),
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
	// Lidarr's Sabnzbd.GetQueue() does a bare `foreach (var item in
	// sabQueue.Items)` with no null check - if this list marshals as JSON
	// `null` (Go's zero value for a nil slice) instead of `[]`, that
	// foreach throws a NullReferenceException. Confirmed against a real
	// production Lidarr this session: it crashed on every periodic queue
	// poll whenever the queue was empty, repeatedly re-tripping Lidarr's
	// own download-client circuit breaker with an escalating backoff.
	resp.Queue.Slots = []sabnzbd.Slot{}
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
			if rate, ok := h.observeSpeed(job); ok {
				totalSpeed += rate
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

// observeSpeed turns two consecutive queue polls into a real byte rate for
// one job, and reports whether it could.
//
// The old calculation was not a rate at all: it took the bytes downloaded so
// far and divided them by a hardcoded 10 seconds, so a 35 MB single that had
// been running for a minute reported 3.5 MB/s and a 5.7 GB box set reported
// "154.00 M" and kbpersec 157696 - numbers Lidarr displays and the external
// stuck-monitor used to decide whether the container was wedged. Two samples
// taken dt apart give (bytesBefore - bytesNow)/dt, which is what the field
// means. The first poll for a job has nothing to compare against and
// honestly reports 0.
func (h *Handler) observeSpeed(job *queue.Job) (float64, bool) {
	now := time.Now()

	h.speedMu.Lock()
	defer h.speedMu.Unlock()
	if h.speedSamples == nil {
		h.speedSamples = make(map[string]speedSample)
	}
	// Bound the map: it is keyed by nzo_id and a long-lived process sees a
	// new id per grab.
	if len(h.speedSamples) > 512 {
		for k, v := range h.speedSamples {
			if now.Sub(v.at) > 10*time.Minute {
				delete(h.speedSamples, k)
			}
		}
	}
	prev, ok := h.speedSamples[job.NzoID]
	h.speedSamples[job.NzoID] = speedSample{sizeleft: job.Sizeleft, at: now}
	if !ok {
		return 0, false
	}
	dt := now.Sub(prev.at).Seconds()
	if dt <= 0 {
		return 0, false
	}
	// Sizeleft only ever grows when a new attempt restarts the job from an
	// empty directory; a negative delta is a restart, not negative speed.
	if moved := prev.sizeleft - job.Sizeleft; moved > 0 {
		return float64(moved) / dt, true
	}
	return 0, true
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

// handlePause pauses one job, or the whole queue when no value is given.
//
// The whole-queue form is real SABnzbd's semantics for
// `mode=queue&name=pause`; this used to answer HTTP 400 for it. Pausing also
// cancels the in-flight download now - the status alone did not stop
// anything, and the worker's next progress event overwrote it.
func (h *Handler) handlePause(c fiber.Ctx) error {
	nzoID := c.Query("value")
	if nzoID == "" {
		return h.handlePauseAll(c)
	}
	job, err := h.queue.Get(nzoID)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(sabnzbd.StatusResponse{
			Status: false, Error: "job not found",
		})
	}
	if job.Status == sabnzbd.StatusDownloading {
		h.CancelJob(nzoID)
	}
	job.Status = sabnzbd.StatusPaused
	if err := h.queue.Update(job); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(sabnzbd.StatusResponse{
			Status: false, Error: err.Error(),
		})
	}
	return c.JSON(sabnzbd.StatusResponse{Status: true, NzoIDs: []string{nzoID}})
}

// handleResume resumes one job, or the whole queue when no value is given
// (real SABnzbd's `mode=queue&name=resume`).
func (h *Handler) handleResume(c fiber.Ctx) error {
	nzoID := c.Query("value")
	if nzoID == "" {
		return h.handleResumeAll(c)
	}
	job, err := h.queue.Get(nzoID)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(sabnzbd.StatusResponse{
			Status: false, Error: "job not found",
		})
	}
	job.Status = sabnzbd.StatusQueued
	job.CompletedAt = nil
	job.StartedAt = nil
	if err := h.queue.Update(job); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(sabnzbd.StatusResponse{
			Status: false, Error: err.Error(),
		})
	}
	h.dispatchJob(job)
	return c.JSON(sabnzbd.StatusResponse{Status: true, NzoIDs: []string{nzoID}})
}

func (h *Handler) handleDelete(c fiber.Ctx) error {
	nzoID := c.Query("value")
	if nzoID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(sabnzbd.StatusResponse{
			Status: false, Error: "missing nzo_id",
		})
	}
	// Cancel the download before dropping the row. Without this the
	// goroutine kept running - and kept its concurrency slot - so with
	// SPF_MAX_CONCURRENT=1 a deleted job stalled every later one for as long
	// as its retries and service fallbacks took.
	h.CancelJob(nzoID)
	h.clearRequeues(nzoID)

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
