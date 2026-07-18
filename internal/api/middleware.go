package api

import (
	"crypto/subtle"
	"net/url"

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

// redactAPIKey replaces the value of an `apikey` query parameter with `***`
// so request logs never contain the actual secret.
func redactAPIKey(query string) string {
	values, err := url.ParseQuery(query)
	if err != nil {
		return query
	}
	if values.Has("apikey") {
		values.Set("apikey", "***")
	}
	return values.Encode()
}
