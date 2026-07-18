package sabnzbd

import (
	"strconv"

	"github.com/gofiber/fiber/v3"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/queue"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/pkg/sabnzbd"
)

func (h *Handler) handleHistory(c fiber.Ctx) error {
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
