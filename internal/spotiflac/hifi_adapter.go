package spotiflac

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
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
}

// hifiManifestBTS is the base64-decoded manifest when manifestMimeType
// is "application/vnd.tidal.bts" (used for LOSSLESS/HIGH/LOW qualities).
type hifiManifestBTS struct {
	MimeType       string `json:"mimeType"`
	Codecs         string `json:"codecs"`
	EncryptionType string `json:"encryptionType"`
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
		client: &http.Client{Timeout: 30 * time.Second},
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
	req, err := http.NewRequest("GET",
		fmt.Sprintf("%s/track/?id=%s&quality=%s", a.upstream, trackID, quality), nil)
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
	if err := json.Unmarshal(body, &hifiResp); err != nil {
		return nil, fmt.Errorf("hifi-adapter: decode hifi response: %w", err)
	}

	if hifiResp.Detail != "" {
		return nil, fmt.Errorf("hifi-adapter: upstream error: %s", hifiResp.Detail)
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
