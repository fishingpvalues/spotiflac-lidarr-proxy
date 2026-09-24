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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	proxyBase  = "http://localhost:8484"
	lidarrBase = "http://localhost:8686"
	apiKey     = "test-integration-key"

	// proxyBaseFromLidarr is how Lidarr's own container reaches the proxy -
	// by docker-compose service name, not localhost (that's only valid from
	// this test process's own host-side view, not from inside another
	// container's network namespace).
	proxyBaseFromLidarr = "http://proxy:8484"
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

	// map[string]any, not map[string]string: the field set is diagnostic
	// and mixed-type - status is a string, break_until is a JSON number
	// (epoch seconds, 0 when no upstream break), session_expires_at is an
	// RFC3339 string or JSON null. A string-only map breaks the moment a
	// non-string field is added (measured 2026-08-27: the integration run
	// failed with "cannot unmarshal number into Go value of type string"
	// the day the operator fields landed).
	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "ok", body["status"])

	// Pin the operator-facing shape at the HTTP level too: break_until is
	// always present and numeric; session_expires_at is present and
	// either a string or null (json decodes null to nil).
	assert.Contains(t, body, "break_until")
	_, ok := body["break_until"].(float64)
	assert.True(t, ok, "break_until must be a number, got %T", body["break_until"])
	assert.Contains(t, body, "session_expires_at")
	switch v := body["session_expires_at"].(type) {
	case string:
		assert.True(t, len(v) > 0, "session_expires_at string must not be empty")
	case nil:
		// no valid community session - expected in CI's fresh container
	default:
		t.Errorf("session_expires_at must be string or null, got %T", v)
	}

	// warnings is always a list. CI's stack configures no registry and no
	// verification solver, which is exactly the setup that cannot download,
	// so it must say so rather than report a bare "ok".
	warnings, ok := body["warnings"].([]any)
	require.True(t, ok, "warnings must be a list, got %T", body["warnings"])
	assert.NotEmpty(t, warnings, "an unconfigured backend must be reported in /health warnings")
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

type lidarrValidationFailure struct {
	IsWarning    bool   `json:"isWarning"`
	ErrorMessage string `json:"errorMessage"`
}

// assertLidarrTestOK verifies a Lidarr /test response succeeded. Lidarr
// returns HTTP 400 for BOTH real errors and pure informational warnings
// alike - e.g. "Sabnzbd develop version, assuming version 3.0.0 or
// higher" is isWarning:true, not a real failure, yet still 400s. The
// isWarning field, not the status code, is what actually distinguishes
// them. toleratedMessages additionally allows specific known-benign
// non-warning messages.
func assertLidarrTestOK(t *testing.T, resp *http.Response, body string, toleratedMessages ...string) {
	t.Helper()
	if resp.StatusCode == 200 {
		return
	}
	var failures []lidarrValidationFailure
	require.NoError(t, json.Unmarshal([]byte(body), &failures), "unexpected non-200 response: %s", body)
	for _, f := range failures {
		if f.IsWarning {
			continue
		}
		tolerated := false
		for _, msg := range toleratedMessages {
			if strings.Contains(f.ErrorMessage, msg) {
				tolerated = true
				break
			}
		}
		assert.True(t, tolerated, "real (non-warning, untolerated) validation failure: %s", f.ErrorMessage)
	}
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
		assertLidarrTestOK(t, resp, body)
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
				{"name": "baseUrl", "value": proxyBaseFromLidarr},
				{"name": "apiPath", "value": "/api/newznab"},
				{"name": "apiKey", "value": apiKey},
				{"name": "categories", "value": []int{3000, 3040}},
			},
		})
		// No tolerance: this must be a green Test. It was red - and
		// tolerated here - for as long as the stack answered the browse
		// feed with an empty result, which is exactly what issue #7
		// reported. The compose file sets SPF_RSS_QUERY so it is not.
		assertLidarrTestOK(t, resp, body)
	})
}

// TestIntegration_BrowseFeedAnswersLidarrTestQuery pins the request Lidarr's
// indexer Test actually sends - t=music with no q, artist or album - and
// fails with a diagnosable message when the feed comes back empty, which
// Lidarr reports as "no results in the configured categories were returned
// from your indexer" (issue #7). The compose stack ships SPF_RSS_QUERY
// (new music friday) for this reason; a deployment without it answers an
// empty feed by design, so this test also documents what "configured" means.
func TestIntegration_BrowseFeedAnswersLidarrTestQuery(t *testing.T) {
	skipIfNoDocker(t)

	resp, err := http.Get(proxyBase + "/api/newznab?t=music&cat=3000,3040&extended=1&apikey=" + apiKey + "&offset=0&limit=100")
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, 200, resp.StatusCode)
	assert.Contains(t, string(body), "<item>",
		"browse feed is empty, so Lidarr's indexer Test fails with 'no results in the configured categories'. Is SPF_RSS_QUERY set on the proxy?")
}
