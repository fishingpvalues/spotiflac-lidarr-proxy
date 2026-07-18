package sabnzbd

import (
	"strconv"

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

	if len(jobs) > 0 {
		resp.Queue.Status = "Downloading"
	}

	free1, total1, err := h.storage.GetDiskSpace()
	if err != nil {
		h.log.Warn().Err(err).Msg("failed to get disk space")
	}
	resp.Queue.Diskspace1 = free1
	resp.Queue.Diskspacetotal1 = total1
	resp.Queue.Diskspace2 = free1
	resp.Queue.Diskspacetotal2 = total1

	for i, job := range jobs {
		slot := jobToSlot(job, i)
		resp.Queue.Slots = append(resp.Queue.Slots, slot)
	}

	return c.JSON(resp)
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
