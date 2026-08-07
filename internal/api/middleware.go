package api

import (
	"crypto/subtle"
	"net/url"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/rs/zerolog"
)

// APIKeyAuthWithSkiplist requires ?apikey= on every request except those
// invoking one of skipModes. Convenience wrapper for groups that serve only
// SABnzbd; see APIKeyAuth for the general form.
func APIKeyAuthWithSkiplist(apiKey string, skipModes ...string) fiber.Handler {
	return APIKeyAuth(apiKey, skipModes, nil)
}

// APIKeyAuth requires ?apikey= on every request except those invoking an
// exempt SABnzbd mode (skipModes, matched against "mode") or an exempt
// newznab type (skipTypes, matched against "t").
//
// The two lists are separate on purpose. A single list used to be matched
// against both parameters, which was an authentication bypass: the group
// mounted at the bare "/api" prefix has to carry newznab's "caps" (fiber
// matches Use() by path prefix, so that middleware also runs for
// /api/newznab/*), and cross-matching then meant appending &t=caps to any
// mode= request skipped authentication entirely.
//
// Verified against production 2026-08-07: GET /api?mode=queue returned 401,
// GET /api?mode=queue&t=caps returned the full queue. mode=get_config,
// mode=addurl and the queue-delete action were reachable the same way, with
// no credentials, from any container that could route to the port.
//
// A request carrying mode= is a SABnzbd request whatever else it carries, so
// only skipModes can exempt it - otherwise the mirror-image trick (sending
// mode=caps to a newznab route) just moves the hole. mode is read from the
// query first and the form body second, matching sabnzbd's own dispatch
// exactly, so the two can never disagree about which mode is being invoked.
func APIKeyAuth(apiKey string, skipModes, skipTypes []string) fiber.Handler {
	return func(c fiber.Ctx) error {
		mode := c.Query("mode")
		if mode == "" {
			mode = c.FormValue("mode")
		}

		if mode != "" {
			if contains(skipModes, mode) {
				return c.Next()
			}
		} else if t := c.Query("t"); t != "" {
			if contains(skipTypes, t) {
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

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
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
