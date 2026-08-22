package sabnzbd

import (
	"strconv"

	"github.com/gofiber/fiber/v3"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/queue"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/pkg/sabnzbd"
)

func (h *Handler) handleHistory(c fiber.Ctx) error {
	// mode=history&name=delete is Lidarr's RemoveFromHistory call - the
	// "history" mode is overloaded with the delete sub-action exactly like
	// "queue" is. Without this branch every RemoveFromHistory silently
	// returned the history list instead of deleting anything.
	if c.Query("name") == "delete" {
		return h.handleHistoryDelete(c)
	}
	start, _ := strconv.Atoi(c.Query("start", "0"))
	limit, _ := strconv.Atoi(c.Query("limit", "50"))

	params := queue.ListParams{
		Start:  start,
		Limit:  limit,
		Search: c.Query("search", ""),
	}

	jobs, total, err := h.queue.History(params)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(sabnzbd.StatusResponse{
			Status: false,
			Error:  err.Error(),
		})
	}

	resp := sabnzbd.HistoryResponse{}
	// Same as queue.go's Slots: Lidarr's Sabnzbd.GetHistory() also does a
	// bare foreach with no null check, so an empty history must marshal
	// as `[]`, never `null`.
	resp.History.Slots = []sabnzbd.HistorySlot{}
	resp.History.Noofslots = total
	resp.History.Version = h.version
	resp.History.MonthSize = "0"
	resp.History.WeekSize = "0"

	var totalSize int64
	for _, job := range jobs {
		totalSize += job.Size
	}
	resp.History.TotalSize = formatBytes(totalSize)

	for _, job := range jobs {
		downloadTime := 0
		if job.CompletedAt != nil {
			downloadTime = int(job.CompletedAt.Sub(job.TimeAdded).Seconds())
		}

		slot := sabnzbd.HistorySlot{
			Status:       string(job.Status),
			NzoID:        job.NzoID,
			Name:         job.Filename,
			Size:         job.Size,
			Cat:          job.Category,
			Category:     job.Category,
			DownloadTime: downloadTime,
			Storage:      job.OutputPath,
			Path:         job.OutputPath,
			Script:       "Default",
			URL:          job.SpotifyURL,
		}
		if job.CompletedAt != nil {
			slot.Completed = job.CompletedAt.Unix()
		}
		if job.Status == sabnzbd.StatusFailed {
			slot.FailMessage = job.ErrorMessage
		}
		resp.History.Slots = append(resp.History.Slots, slot)
	}

	return c.JSON(resp)
}

// handleHistoryDelete implements mode=history&name=delete (Lidarr's
// RemoveFromHistory). Real SABnzbd either archives or hard-deletes the entry;
// this flat model has no archive table, so both paths delete the row.
// del_files additionally removes the on-disk job directory.
func (h *Handler) handleHistoryDelete(c fiber.Ctx) error {
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
			h.log.Warn().Err(err).Str("nzo_id", nzoID).Msg("failed to cleanup history job files")
		}
	}
	return c.JSON(sabnzbd.StatusResponse{Status: true, NzoIDs: []string{nzoID}})
}
