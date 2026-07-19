package integration_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	proxyBase  = "http://localhost:8484"
	lidarrBase = "http://localhost:8686"
	apiKey     = "test-integration-key"
)

func skipIfNoDocker(t *testing.T) {
	t.Helper()
	if os.Getenv("INTEGRATION") != "1" {
		t.Skip("set INTEGRATION=1 to run integration tests (requires docker-compose up)")
	}
}

func TestIntegration_ProxyHealth(t *testing.T) {
	skipIfNoDocker(t)

	resp, err := http.Get(proxyBase + "/health")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, 200, resp.StatusCode)

	var body map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "ok", body["status"])
}

func TestIntegration_SABnzbdVersion(t *testing.T) {
	skipIfNoDocker(t)

	url := fmt.Sprintf("%s/api/sabnzbd?mode=version", proxyBase)
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, 200, resp.StatusCode)

	var v map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&v))
	assert.Contains(t, v, "version")
}

func TestIntegration_SABnzbdAddURLAndQueue(t *testing.T) {
	skipIfNoDocker(t)

	addURL := fmt.Sprintf("%s/api/sabnzbd?mode=addurl&name=https://open.spotify.com/album/0sNOF9WDwhWunNAHPD3Baj&apikey=%s", proxyBase, apiKey)
	resp, err := http.Post(addURL, "application/x-www-form-urlencoded", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)

	var addResp struct {
		Status bool     `json:"status"`
		NzoIDs []string `json:"nzo_ids"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&addResp))
	assert.True(t, addResp.Status)
	require.NotEmpty(t, addResp.NzoIDs)
	nzoID := addResp.NzoIDs[0]

	time.Sleep(2 * time.Second)

	queueURL := fmt.Sprintf("%s/api/sabnzbd?mode=queue&nzo_ids=%s&apikey=%s", proxyBase, nzoID, apiKey)
	resp2, err := http.Get(queueURL)
	require.NoError(t, err)
	defer resp2.Body.Close()
	assert.Equal(t, 200, resp2.StatusCode)

	var q struct {
		Queue struct {
			Slots []struct {
				NzoID  string `json:"nzo_id"`
				Status string `json:"status"`
			} `json:"slots"`
		} `json:"queue"`
	}
	require.NoError(t, json.NewDecoder(resp2.Body).Decode(&q))
	assert.NotEmpty(t, q.Queue.Slots)
	assert.Equal(t, nzoID, q.Queue.Slots[0].NzoID)
}

// lidarrConfigAPIKeyPattern extracts Lidarr's auto-generated API key from
// its own config.xml. The unauthenticated /initialize.js bootstrap trick
// other *arr automation uses doesn't work here: Lidarr 401s that endpoint
// once AuthenticationMethod is Forms (confirmed against a real production
// instance - and a fresh container logs "UI/initialize.js not found"
// regardless of auth, since this image doesn't ship the web UI bundle
// config.xml is always readable directly, auth or not.
var lidarrConfigAPIKeyPattern = regexp.MustCompile(`<ApiKey>([^<]+)</ApiKey>`)

// fetchLidarrAPIKey reads Lidarr's own API key. This is NOT the proxy's
// SPF_API_KEY - Lidarr generates its own, separate key on first boot, and
// every /api/v1/* call must authenticate with that one, not the proxy's.
func fetchLidarrAPIKey(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("docker", "compose", "exec", "-T", "lidarr", "cat", "/config/config.xml").Output()
	require.NoError(t, err, "reading Lidarr's config.xml via docker compose exec")

	match := lidarrConfigAPIKeyPattern.FindSubmatch(out)
	require.NotNil(t, match, "could not find ApiKey in Lidarr's config.xml")
	return string(match[1])
}

// lidarrRequest posts a JSON body to a Lidarr v1 API endpoint, authenticated
// with Lidarr's own key (see fetchLidarrAPIKey), and returns the response
// alongside its raw response body (a string, not a parsed type - success
// responses are `{}`, validation failures are a `[{...}]` array; forcing
// either shape into a fixed Go type would hide the real error on failure).
func lidarrRequest(t *testing.T, lidarrAPIKey, path string, body map[string]any) (*http.Response, string) {
	t.Helper()
	bodyJSON, err := json.Marshal(body)
	require.NoError(t, err)

	req, err := http.NewRequest("POST", lidarrBase+path, bytes.NewReader(bodyJSON))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", lidarrAPIKey)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, string(respBody)
}

// TestIntegration_LidarrConfiguresProxy exercises the exact setup steps from
// the README: adding the proxy as a Lidarr SABnzbd download client and a
// Newznab indexer, using the real DownloadClientResource/IndexerResource
// shape (a flat {host,port,apiKey} body 400s - Lidarr expects a `fields`
// array). Verified against a real production Lidarr instance before writing
// this: /api/v1/downloadclient/test and /api/v1/indexer/test return `{}` on
// success.
func TestIntegration_LidarrConfiguresProxy(t *testing.T) {
	skipIfNoDocker(t)

	lidarrKey := fetchLidarrAPIKey(t)

	t.Run("download client", func(t *testing.T) {
		resp, body := lidarrRequest(t, lidarrKey, "/api/v1/downloadclient/test", map[string]any{
			"enable":             true,
			"protocol":           "usenet",
			"priority":           1,
			"name":               "SpotiFLAC Proxy",
			"implementation":     "Sabnzbd",
			"implementationName": "SABnzbd",
			"configContract":     "SabnzbdSettings",
			"fields": []map[string]any{
				{"name": "host", "value": "proxy"},
				{"name": "port", "value": 8484},
				{"name": "apiKey", "value": apiKey},
				{"name": "urlBase", "value": ""},
				{"name": "musicCategory", "value": "music"},
			},
		})
		assert.Equal(t, 200, resp.StatusCode, "downloadclient/test response body: %v", body)
	})

	t.Run("indexer", func(t *testing.T) {
		resp, body := lidarrRequest(t, lidarrKey, "/api/v1/indexer/test", map[string]any{
			"enable":             true,
			"protocol":           "usenet",
			"priority":           25,
			"name":               "SpotiFLAC Proxy",
			"implementation":     "Newznab",
			"implementationName": "Newznab",
			"configContract":     "NewznabSettings",
			"fields": []map[string]any{
				{"name": "baseUrl", "value": proxyBase},
				{"name": "apiPath", "value": "/api/newznab"},
				{"name": "apiKey", "value": apiKey},
				{"name": "categories", "value": []int{3010, 3040}},
			},
		})
		assert.Equal(t, 200, resp.StatusCode, "indexer/test response body: %v", body)
	})
}
