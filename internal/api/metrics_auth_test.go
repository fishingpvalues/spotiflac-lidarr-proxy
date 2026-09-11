package api_test

import (
	"net/http"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/api"
)

// APIKeyOnly is what guards endpoints outside the SABnzbd/Newznab dispatch,
// where there is no mode= or t= that could legitimately exempt a request. The
// whole point is that nothing exempts it, so that is what is asserted.
func TestAPIKeyOnlyHasNoExemptions(t *testing.T) {
	app := fiber.New()
	app.Get("/metrics", api.APIKeyOnly("secret"), func(c fiber.Ctx) error {
		return c.SendString("metrics")
	})

	cases := []struct {
		name string
		url  string
		want int
	}{
		{"no key", "/metrics", http.StatusUnauthorized},
		{"empty key", "/metrics?apikey=", http.StatusUnauthorized},
		{"wrong key", "/metrics?apikey=nope", http.StatusUnauthorized},
		// The exemptions that work on the SABnzbd and Newznab groups must
		// not work here - those skiplists exist for Lidarr's unauthenticated
		// probes, and nothing probes /metrics.
		{"mode=version does not exempt", "/metrics?mode=version", http.StatusUnauthorized},
		{"mode=auth does not exempt", "/metrics?mode=auth", http.StatusUnauthorized},
		{"t=caps does not exempt", "/metrics?t=caps", http.StatusUnauthorized},
		{"correct key", "/metrics?apikey=secret", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest("GET", tc.url, nil)
			require.NoError(t, err)
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, tc.want, resp.StatusCode)
		})
	}
}
