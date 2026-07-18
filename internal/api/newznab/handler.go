package newznab

import (
	"github.com/gofiber/fiber/v3"
	"github.com/rs/zerolog"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/indexer"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
)

type Handler struct {
	client    *spotiflac.Client
	log       zerolog.Logger
	serverURL string
}

func NewHandler(client *spotiflac.Client, serverURL string) *Handler {
	return &Handler{
		client:    client,
		log:       zerolog.Nop(),
		serverURL: serverURL,
	}
}

func (h *Handler) SetLogger(log zerolog.Logger) {
	h.log = log
}

func (h *Handler) RegisterRoutes(app *fiber.App) {
	h.RegisterRoutesOnGroup(app.Group("/api/newznab"))
}

func (h *Handler) RegisterRoutesOnGroup(group fiber.Router) {
	group.Get("/", h.dispatch)
}

func (h *Handler) dispatch(c fiber.Ctx) error {
	t := c.Query("t")
	switch t {
	case "caps":
		return h.handleCaps(c)
	case "search":
		return h.handleSearch(c)
	case "music":
		return h.handleMusic(c)
	case "details":
		return h.handleDetails(c)
	default:
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "unknown t parameter: " + t,
		})
	}
}

func (h *Handler) handleCaps(c fiber.Ctx) error {
	c.Set("Content-Type", "application/xml")
	return c.Send(indexer.CapsXML(h.serverURL))
}

func (h *Handler) handleSearch(c fiber.Ctx) error {
	query := c.Query("q")
	artist := c.Query("artist")
	album := c.Query("album")

	results, err := indexer.Search(c.Context(), h.client, query, artist, album)
	if err != nil {
		h.log.Error().Err(err).Msg("newznab search failed")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "search failed",
		})
	}

	xml, err := indexer.NewznabXML(results, h.serverURL)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "xml generation failed",
		})
	}

	c.Set("Content-Type", "application/rss+xml")
	return c.Send(xml)
}

func (h *Handler) handleMusic(c fiber.Ctx) error {
	artist := c.Query("artist")
	album := c.Query("album")

	query := artist
	if album != "" {
		query = artist + " " + album
	}

	results, err := indexer.Search(c.Context(), h.client, query, artist, album)
	if err != nil {
		h.log.Error().Err(err).Msg("newznab music search failed")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "music search failed",
		})
	}

	xml, err := indexer.NewznabXML(results, h.serverURL)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "xml generation failed",
		})
	}

	c.Set("Content-Type", "application/rss+xml")
	return c.Send(xml)
}

func (h *Handler) handleDetails(c fiber.Ctx) error {
	// Lidarr expects RSS XML for details too — single item
	id := c.Query("id")
	results, err := indexer.Search(c.Context(), h.client, id, "", "")
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "details search failed",
		})
	}

	xml, err := indexer.NewznabXML(results, h.serverURL)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "xml generation failed",
		})
	}

	c.Set("Content-Type", "application/rss+xml")
	return c.Send(xml)
}
