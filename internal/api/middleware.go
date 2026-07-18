package api

import (
	"github.com/gofiber/fiber/v3"
	"github.com/rs/zerolog"
)

func APIKeyAuth(apiKey string) fiber.Handler {
	return func(c fiber.Ctx) error {
		key := c.Query("apikey")
		if key == "" {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "API Key Required",
			})
		}
		if key != apiKey {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "API Key Incorrect",
			})
		}
		return c.Next()
	}
}

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
		if key != apiKey {
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
			Str("query", string(c.Request().URI().QueryString())).
			Int("status", c.Response().StatusCode()).
			Msg("request")
		return err
	}
}
