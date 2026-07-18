package sabnzbd

import (
	"github.com/gofiber/fiber/v3"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/pkg/sabnzbd"
)

func (h *Handler) handleVersion(c fiber.Ctx) error {
	return c.JSON(sabnzbd.VersionResponse{Version: h.version})
}

func (h *Handler) handleAuth(c fiber.Ctx) error {
	return c.JSON(sabnzbd.AuthResponse{Auth: true})
}

func (h *Handler) handleGetConfig(c fiber.Ctx) error {
	resp := sabnzbd.ConfigResponse{}
	resp.Config.Categories = []sabnzbd.Category{
		{Name: "music", Order: 0, Dir: "music"},
		{Name: "music-flac-16", Order: 0, Dir: "music-flac-16"},
		{Name: "music-flac-24", Order: 1, Dir: "music-flac-24"},
		{Name: "music-mp3", Order: 2, Dir: "music-mp3"},
	}
	resp.Config.Scripts = []sabnzbd.Script{
		{Name: "Default", Default: true},
	}
	resp.Config.Speedlimit = "100"
	resp.Config.Misc.Version = h.version
	resp.Config.Misc.CompletedDir = h.cfg.OutputDir
	resp.Config.Misc.CompleteDirEnabled = true
	resp.Config.Misc.PreCheck = false
	resp.Config.Misc.HistoryRetention = "all"
	return c.JSON(resp)
}

func (h *Handler) handleFullStatus(c fiber.Ctx) error {
	return c.JSON(sabnzbd.FullStatusResponse{
		CompleteDir: h.cfg.OutputDir,
	})
}

func (h *Handler) handleGetCats(c fiber.Ctx) error {
	return c.JSON(sabnzbd.CategoriesResponse{
		Categories: []string{"music", "music-flac-16", "music-flac-24", "music-mp3"},
	})
}
