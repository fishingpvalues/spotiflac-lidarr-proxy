package integration_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
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
	json.NewDecoder(resp.Body).Decode(&body)
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
	json.NewDecoder(resp.Body).Decode(&v)
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
	json.NewDecoder(resp.Body).Decode(&addResp)
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
	json.NewDecoder(resp2.Body).Decode(&q)
	assert.NotEmpty(t, q.Queue.Slots)
	assert.Equal(t, nzoID, q.Queue.Slots[0].NzoID)
}

func TestIntegration_LidarrConfiguresProxy(t *testing.T) {
	skipIfNoDocker(t)

	url := fmt.Sprintf("%s/api/v1/downloadclient/test", lidarrBase)
	body := map[string]interface{}{
		"enable":   true,
		"protocol": "usenet",
		"name":     "Spotiflac Proxy",
		"host":     "proxy",
		"port":     8484,
		"apiKey":   apiKey,
		"urlBase":  "/api/sabnzbd",
	}
	bodyJSON, _ := json.Marshal(body)

	req, _ := http.NewRequest("POST", url, bytes.NewReader(bodyJSON))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", apiKey)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	t.Logf("Lidarr test connection status: %d", resp.StatusCode)
}
