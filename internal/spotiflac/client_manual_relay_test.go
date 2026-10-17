package spotiflac_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/api"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
)

// A real manual callback must pass the same SSRF guard as an automatic one.
// Previously the CLI event was only recorded when FSL was configured, so
// /api/verify-relay rejected every grant returned from a person's browser.
func TestManualVerificationRelayWithoutFSL(t *testing.T) {
	t.Setenv("SPOTIFLAC_FSL_URL", "")
	t.Setenv("SPF_SESSION_RENEW_CMD", "")
	grants := make(chan string, 1)
	listener := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/session-grant", r.URL.Path)
		assert.Equal(t, "manual-state", r.URL.Query().Get("state"))
		grants <- r.URL.Query().Get("grant")
		w.WriteHeader(http.StatusOK)
	}))
	defer listener.Close()

	upstream := listener.URL + "/session-grant?state=manual-state"
	relay := "http://proxy.example/api/verify-relay?upstream_cb=" + url.QueryEscape(upstream)
	challenge := "https://verify.example/challenge?cb=" + url.QueryEscape(relay)
	line, err := json.Marshal(spotiflac.ProgressEvent{Type: "verification_required", URL: challenge, CB: upstream})
	require.NoError(t, err)
	cli := mockCLIForCascade(t, []string{string(line)}, 0)
	client := spotiflac.NewClient(cli, 5*time.Second, "tidal", "lossless", "http://proxy.example/api/verify-relay", "", "", nil, "", nil)

	app := fiber.New()
	app.Get("/api/verify-relay", api.NewVerifyRelayHandler(client).Handle)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, errs := client.DownloadCLI(ctx, "https://open.spotify.com/track/test", t.TempDir(), "tidal", "lossless")
	seen := false
	for event := range events {
		if event.Type != "verification_required" {
			continue
		}
		seen = true
		// Test immediately on receipt: recording must precede publication.
		recorded, ok := client.LookupUpstreamCB("manual-state")
		require.True(t, ok)
		assert.Equal(t, upstream, recorded)
		req := httptest.NewRequest(http.MethodGet, "/api/verify-relay?upstream_cb="+url.QueryEscape(upstream)+"&grant=test-grant", nil)
		resp, err := app.Test(req)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "test-grant", <-grants)
	}
	for err := range errs {
		require.NoError(t, err)
	}
	require.True(t, seen, "the mock CLI must publish a verification request")
}
