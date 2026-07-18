package sabnzbd

import (
	"github.com/gofiber/fiber/v3"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/pkg/sabnzbd"
)

func (h *Handler) handleServerStats(c fiber.Ctx) error {
	return c.JSON(sabnzbd.ServerStatsResponse{
		Total:      0,
		Month:      0,
		Week:       0,
		Daily:      0,
		Articles:   0,
		Speed:      "0",
		Version:    h.version,
		Day:        0,
		WeekAccel:  0,
		MonthAccel: 0,
	})
}
