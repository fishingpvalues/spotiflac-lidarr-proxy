package api

import (
	"crypto/subtle"
	"net/url"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/rs/zerolog"
)

func APIKeyAuthWithSkiplist(apiKey string, skipModes ...string) fiber.Handler {
	return func(c fiber.Ctx) error {
		// Check both "mode" (sabnzbd) and "t" (newznab) parameters
		mode := c.Query("mode")
		if mode == "" {
			mode = c.FormValue("mode")
		}
		t := c.Query("t")
		for _, skip := range skipModes {
			if mode == skip || t == skip {
				return c.Next()
			}
		}
		key := c.Query("apikey")
		if key == "" {
			key = c.FormValue("apikey")
		}
		if key == "" {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "API Key Required",
			})
		}
		if subtle.ConstantTimeCompare([]byte(key), []byte(apiKey)) != 1 {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "API Key Incorrect",
			})
		}
		return c.Next()
	}
}

func RequestLogger(log zerolog.Logger) fiber.Handler {
	return func(c fiber.Ctx) error {
		err := c.Next()
		log.Info().
			Str("method", c.Method()).
			Str("path", c.Path()).
			Str("query", redactAPIKey(string(c.Request().URI().QueryString()))).
			Int("status", c.Response().StatusCode()).
			Msg("request")
		return err
	}
}

// redactAPIKey replaces the value of an "apikey" query parameter with "***"
// so request logs never contain the actual secret, even if some other
// unrelated query parameter fails to URL-decode. Works on raw key=value
// segments split by "&" rather than a full url.ParseQuery, so a decode
// failure elsewhere in the query string can never suppress redaction.
func redactAPIKey(query string) string {
	if query == "" {
		return query
	}
	segments := strings.Split(query, "&")
	for i, seg := range segments {
		key, _, found := strings.Cut(seg, "=")
		if !found {
			continue
		}
		if unescaped, err := url.QueryUnescape(key); err == nil {
			key = unescaped
		}
		if key == "apikey" {
			segments[i] = "apikey=***"
		}
	}
	return strings.Join(segments, "&")
}
