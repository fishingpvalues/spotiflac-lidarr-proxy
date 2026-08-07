package spotiflac

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// playbackQueueWait bounds how long we wait on a 202 queue ticket.
	// Upstream drops the request record after five minutes; stopping short
	// of that keeps the error message accurate instead of "404".
	playbackQueueWait = 4 * time.Minute
	// playbackPollInterval is how often the ticket is polled. Upstream
	// suggests Retry-After but does not always send it.
	playbackPollInterval = 3 * time.Second
)

// hifiTrackResponse is the JSON response from a hifi-api /track/ endpoint.
// Format: {"data": {"manifest": "base64...", "manifestMimeType": "...", ...}}
type hifiTrackResponse struct {
	Data struct {
		TrackID          int    `json:"trackId"`
		AudioQuality     string `json:"audioQuality"`
		ManifestMimeType string `json:"manifestMimeType"`
		Manifest         string `json:"manifest"`
		BitDepth         int    `json:"bitDepth"`
		SampleRate       int    `json:"sampleRate"`
	} `json:"data"`
	Detail string `json:"detail"` // error detail

	// Playback queue ticket, returned with HTTP 202 when every playback
	// credential upstream is already serving a request.
	Status    string `json:"status"`
	RequestID string `json:"requestId"`
	StatusURL string `json:"statusUrl"`
	Queue     int    `json:"queuePosition"`
}

// hifiManifestBTS is the base64-decoded manifest when manifestMimeType
// is "application/vnd.tidal.bts" (used for LOSSLESS/HIGH/LOW qualities).
type hifiManifestBTS struct {
	MimeType       string   `json:"mimeType"`
	Codecs         string   `json:"codecs"`
	EncryptionType string   `json:"encryptionType"`
	URLs           []string `json:"urls"`
}

// spotiflacTrackResponse is the format SpotiFLAC CLI expects from a custom
// Tidal API URL: {"url": "<direct-download-url>", ...}
type spotiflacTrackResponse struct {
	URL     string `json:"url"`
	Quality string `json:"quality,omitempty"`
}

// HiFiAdapter translates between hifi-api format (manifest-based) and
// SpotiFLAC-compatible format (direct URL). Implements the same
// /track/?id=X&quality=Y endpoint that SpotiFLAC's --tidal-api-url expects.
type HiFiAdapter struct {
	upstream string // hifi-api base URL (e.g. https://api.monochrome.tf)
	client   *http.Client
}

// NewHiFiAdapter creates an adapter that proxies requests to a hifi-api
// instance and converts manifest responses to direct download URLs.
func NewHiFiAdapter(upstream string) *HiFiAdapter {
	return &HiFiAdapter{
		upstream: upstream,
		client:   &http.Client{Timeout: 30 * time.Second},
	}
}

// BaseURL returns the upstream hifi-api base URL.
func (a *HiFiAdapter) BaseURL() string {
	return a.upstream
}

// ResolveTrackURL fetches a track from the upstream hifi-api, decodes the
// manifest, and returns a direct download URL in SpotiFLAC-compatible format.
// Called by our local adapter HTTP handler.
func (a *HiFiAdapter) ResolveTrackURL(trackID, quality string) (*spotiflacTrackResponse, error) {
	hifiResp, err := a.get(fmt.Sprintf("%s/track/?id=%s&quality=%s", a.upstream, trackID, quality))
	if err != nil {
		return nil, err
	}

	// hifi-api 2.x limits each playback credential to one in-flight request.
	// When they are all busy it answers 202 with a queue ticket instead of
	// holding the connection open. The old code saw no manifest field and
	// reported "empty manifest in response", turning ordinary contention
	// into a hard download failure - so wait for the ticket instead.
	if hifiResp.Status == "pending" && hifiResp.StatusURL != "" {
		hifiResp, err = a.awaitPlayback(hifiResp.StatusURL)
		if err != nil {
			return nil, err
		}
	}

	if hifiResp.Data.Manifest == "" {
		return nil, fmt.Errorf("hifi-adapter: empty manifest in response")
	}

	// Decode manifest based on mime type.
	manifestBytes, err := base64.StdEncoding.DecodeString(hifiResp.Data.Manifest)
	if err != nil {
		return nil, fmt.Errorf("hifi-adapter: base64 decode manifest: %w", err)
	}

	switch hifiResp.Data.ManifestMimeType {
	case "application/vnd.tidal.bts":
		return decodeBTSManifest(manifestBytes, hifiResp.Data.AudioQuality)
	case "application/dash+xml":
		// MPD manifests for HI_RES_LOSSLESS — extract first BaseURL
		return decodeMPDManifest(manifestBytes, hifiResp.Data.AudioQuality)
	default:
		return nil, fmt.Errorf("hifi-adapter: unknown manifest type: %s", hifiResp.Data.ManifestMimeType)
	}
}

// get performs one upstream GET and decodes the JSON envelope, surfacing
// transport, status and application-level errors distinctly. A non-2xx that
// is not the 202 queue ticket is reported with a body snippet, because
// upstream returns 403 {"detail":"Upstream API error"} when Tidal has blocked
// the instance's account - a condition no amount of retrying fixes, and one
// that is unreadable if it only ever surfaces as a JSON decode failure.
func (a *HiFiAdapter) get(rawURL string) (*hifiTrackResponse, error) {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("hifi-adapter: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "spotiflac-lidarr-proxy/1.0")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hifi-adapter: upstream request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB max
	if err != nil {
		return nil, fmt.Errorf("hifi-adapter: read body: %w", err)
	}

	var hifiResp hifiTrackResponse
	decodeErr := json.Unmarshal(bytes.TrimSpace(body), &hifiResp)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail := hifiResp.Detail
		if detail == "" {
			detail = snippet(body)
		}
		return nil, fmt.Errorf("hifi-adapter: upstream HTTP %d: %s", resp.StatusCode, detail)
	}
	if decodeErr != nil {
		return nil, fmt.Errorf("hifi-adapter: decode hifi response: %w (body: %s)", decodeErr, snippet(body))
	}
	if hifiResp.Detail != "" {
		return nil, fmt.Errorf("hifi-adapter: upstream error: %s", hifiResp.Detail)
	}
	return &hifiResp, nil
}

// awaitPlayback polls a 202 queue ticket until upstream returns the real
// playback response. Upstream expires request records after five minutes, so
// polling past that only ever yields 404 - stop before then and let the
// caller fall through to the next backend in the cascade.
func (a *HiFiAdapter) awaitPlayback(statusURL string) (*hifiTrackResponse, error) {
	// statusUrl is documented as a path ("/playback/requests/<id>").
	if strings.HasPrefix(statusURL, "/") {
		statusURL = a.upstream + statusURL
	}

	deadline := time.Now().Add(playbackQueueWait)
	for attempt := 0; ; attempt++ {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("hifi-adapter: playback queue did not clear within %s", playbackQueueWait)
		}
		time.Sleep(playbackPollInterval)

		resp, err := a.get(statusURL)
		if err != nil {
			return nil, err
		}
		switch resp.Status {
		case "pending", "processing":
			continue
		case "failed", "cancelled":
			return nil, fmt.Errorf("hifi-adapter: playback request %s", resp.Status)
		default:
			// Completed: the poll carries the original playback response.
			return resp, nil
		}
	}
}

func snippet(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}

func decodeBTSManifest(data []byte, quality string) (*spotiflacTrackResponse, error) {
	var bts hifiManifestBTS
	if err := json.Unmarshal(data, &bts); err != nil {
		return nil, fmt.Errorf("decode BTS manifest: %w", err)
	}
	if len(bts.URLs) == 0 {
		return nil, fmt.Errorf("no URLs in BTS manifest")
	}
	return &spotiflacTrackResponse{
		URL:     bts.URLs[0],
		Quality: quality,
	}, nil
}

func decodeMPDManifest(data []byte, quality string) (*spotiflacTrackResponse, error) {
	// Simple extraction: find first <BaseURL> tag content.
	// Full MPD parsing would require xml.Unmarshal into MPD structs,
	// but for SpotiFLAC we only need the first audio URL.
	start := bytes.Index(data, []byte("<BaseURL>"))
	if start < 0 {
		return nil, fmt.Errorf("no BaseURL in MPD manifest")
	}
	start += len("<BaseURL>")
	end := bytes.Index(data[start:], []byte("</BaseURL>"))
	if end < 0 {
		return nil, fmt.Errorf("unclosed BaseURL in MPD manifest")
	}
	url := string(data[start : start+end])
	return &spotiflacTrackResponse{
		URL:     url,
		Quality: quality,
	}, nil
}
