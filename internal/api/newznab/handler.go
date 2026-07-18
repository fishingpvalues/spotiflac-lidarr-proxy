package newznab

import (
	"strconv"

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
	case "get":
		return h.handleDetails(c)
	case "tvsearch", "movie", "book":
		// Unsupported search types - return empty results gracefully
		return h.handleEmptyResults(c)
	default:
		if t == "" {
			return h.handleEmptyResults(c)
		}
		return h.handleEmptyResults(c)
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

	// Support maxage parameter
	// maxage := c.Query("maxage", "0")

	results, err := indexer.Search(c.Context(), h.client, query, artist, album)
	if err != nil {
		h.log.Error().Err(err).Msg("newznab search failed")
		return h.handleEmptyResults(c)
	}

	// Parse offset/limit for pagination
	offset, _ := strconv.Atoi(c.Query("offset", "0"))
	limit, _ := strconv.Atoi(c.Query("limit", "100"))

	if offset > 0 || limit < len(results) {
		if offset >= len(results) {
			results = nil
		} else {
			end := offset + limit
			if end > len(results) {
				end = len(results)
			}
			results = results[offset:end]
		}
	}

	xml, err := indexer.NewznabXML(results, h.serverURL)
	if err != nil {
		h.log.Error().Err(err).Msg("newznab xml generation failed")
		return h.handleEmptyResults(c)
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
		return h.handleEmptyResults(c)
	}

	xml, err := indexer.NewznabXML(results, h.serverURL)
	if err != nil {
		h.log.Error().Err(err).Msg("newznab xml generation failed")
		return h.handleEmptyResults(c)
	}

	c.Set("Content-Type", "application/rss+xml")
	return c.Send(xml)
}

func (h *Handler) handleDetails(c fiber.Ctx) error {
	id := c.Query("id")
	results, err := indexer.Search(c.Context(), h.client, id, "", "")
	if err != nil {
		return h.handleEmptyResults(c)
	}

	xml, err := indexer.NewznabXML(results, h.serverURL)
	if err != nil {
		return h.handleEmptyResults(c)
	}

	c.Set("Content-Type", "application/rss+xml")
	return c.Send(xml)
}

func (h *Handler) handleEmptyResults(c fiber.Ctx) error {
	xml, err := indexer.NewznabXML(nil, h.serverURL)
	if err != nil {
		return c.SendString("")
	}
	c.Set("Content-Type", "application/rss+xml")
	return c.Send(xml)
}
