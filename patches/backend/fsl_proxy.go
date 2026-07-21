package backend

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// FSLURL returns the FlareSolverr/Byparr API endpoint from SPOTIFLAC_FSL_URL.
// Empty means FSL proxy is not configured.
func FSLURL() string {
	return strings.TrimRight(os.Getenv("SPOTIFLAC_FSL_URL"), "/")
}

// FSLAddress returns the container's own routable address from SPOTIFLAC_ADDRESS,
// or empty if FSL is not configured.
func FSLAddress() string {
	return strings.TrimSpace(os.Getenv("SPOTIFLAC_ADDRESS"))
}

// FSLEnabled returns true when a FlareSolverr/Byparr proxy is configured.
func FSLEnabled() bool {
	return FSLURL() != ""
}

// fslResponse wraps the Byparr/FlareSolverr /v1 response fields we care about.
type fslResponse struct {
	Solution *fslSolution `json:"solution,omitempty"`
	Status   string       `json:"status,omitempty"`
	Message  string       `json:"message,omitempty"`
}

type fslSolution struct {
	URL      string            `json:"url,omitempty"`
	Status   int               `json:"status,omitempty"`
	Response string            `json:"response,omitempty"`
	Cookies  []fslCookie       `json:"cookies,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
}

type fslCookie struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Domain   string `json:"domain"`
	Path     string `json:"path"`
	Secure   bool   `json:"secure"`
	HTTPOnly bool   `json:"httpOnly"`
}

// FSLRequest sends a URL through the Byparr/FlareSolverr proxy and returns
// the response body, HTTP status, and any error. The headless browser
// at the FSL endpoint automatically solves Cloudflare Turnstile challenges.
// Byparr only supports GET requests; POST body is silently ignored.
func FSLRequest(method, targetURL string, body []byte, timeout time.Duration) ([]byte, int, error) {
	fslBase := FSLURL()
	if fslBase == "" {
		return nil, 0, fmt.Errorf("fsl proxy not configured: SPOTIFLAC_FSL_URL is empty")
	}

	// Byparr only supports GET. We always send "request.get" since the
	// challenge URL is loaded in Byparr's browser and the Turnstile
	// callback returns the grant on our own callback listener.
	payload := map[string]interface{}{
		"url":         targetURL,
		"max_timeout": int(timeout.Seconds()),
	}

	raw, _ := json.Marshal(payload)

	httpResp, err := http.Post(fslBase+"/v1", "application/json", bytes.NewReader(raw))
	if err != nil {
		return nil, 0, fmt.Errorf("fsl request failed: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, _ := io.ReadAll(httpResp.Body)

	if httpResp.StatusCode != http.StatusOK {
		return respBody, httpResp.StatusCode,
			fmt.Errorf("fsl returned HTTP %d: %s", httpResp.StatusCode, previewBytes(respBody, 256))
	}

	var fslResp fslResponse
	if err := json.Unmarshal(respBody, &fslResp); err != nil {
		return respBody, httpResp.StatusCode,
			fmt.Errorf("fsl decode error: %w", err)
	}

	if fslResp.Status == "error" || fslResp.Status == "failed" {
		msg := fslResp.Message
		if msg == "" {
			msg = "fsl reported failure"
		}
		return respBody, 502, fmt.Errorf("fsl error: %s", msg)
	}

	if fslResp.Solution == nil {
		return respBody, 502, fmt.Errorf("fsl response missing solution")
	}

	sol := fslResp.Solution
	if sol.Status != 200 {
		return []byte(sol.Response), sol.Status, nil
	}

	return []byte(sol.Response), sol.Status, nil
}

func previewBytes(data []byte, maxLen int) string {
	s := strings.TrimSpace(string(data))
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}
