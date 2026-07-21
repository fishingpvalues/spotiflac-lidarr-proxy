package spotiflac

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/config"
)

type Client struct {
	cliPath        string
	timeout        time.Duration
	defaultService string
	defaultQuality string
	verifyRelayURL string
	tidalAPIURL    string
	qobuzAPIURL    string
	fslURL         string
	relayAddress   string
	relayPort      int

	// tidalAPIFallbacks is a list of additional Tidal API proxy URLs
	// tried in order when the primary tidalAPIURL fails.
	tidalAPIFallbacks []string

	// resolvedTidalAPI caches the last known working Tidal API URL.
	resolvedTidalAPI   string
	resolvedTidalCheck time.Time

	// verificationStates maps state param → upstream_cb URL for
	// community verification relay forwarding (FSL/Byparr path).
	verificationStates sync.Map
}

func NewClient(cliPath string, timeout time.Duration, defaultService, defaultQuality, verifyRelayURL, tidalAPIURL, qobuzAPIURL string, tidalAPIFallbacks []string) *Client {
	fslURL := os.Getenv("SPOTIFLAC_FSL_URL")
	relayAddress := os.Getenv("SPOTIFLAC_ADDRESS")

	return &Client{
		cliPath:            cliPath,
		timeout:            timeout,
		defaultService:     defaultService,
		defaultQuality:     defaultQuality,
		verifyRelayURL:     verifyRelayURL,
		tidalAPIURL:        tidalAPIURL,
		qobuzAPIURL:        qobuzAPIURL,
		fslURL:             fslURL,
		relayAddress:       relayAddress,
		tidalAPIFallbacks:  tidalAPIFallbacks,
	}
}

// isHiFiAPI checks whether a URL hosts a hifi-api instance (manifest-based
// format) rather than a SpotiFLAC-compatible API (direct URL format).
// hifi-api root responds with {"version":"2.X","Repo":"..."}
func isHiFiAPI(baseURL string) bool {
	req, _ := http.NewRequest("GET", baseURL+"/", nil)
	req.Header.Set("User-Agent", "spotiflac-lidarr-proxy/1.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var check struct {
		Version string `json:"version"`
		Repo    string `json:"Repo"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 512)).Decode(&check); err != nil {
		return false
	}
	return check.Version != "" && check.Repo != ""
}

// startHiFiAdapter starts a local HTTP server that translates between
// hifi-api manifest format and SpotiFLAC-compatible direct URL format.
// Returns the address (host:port) to pass as --tidal-api-url.
func (c *Client) startHiFiAdapter(upstream string) (string, error) {
	adapter := NewHiFiAdapter(upstream)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		trackID := r.URL.Query().Get("id")
		quality := r.URL.Query().Get("quality")
		if trackID == "" {
			http.Error(w, "missing id", http.StatusBadRequest)
			return
		}
		if quality == "" {
			quality = "LOSSLESS"
		}

		result, err := adapter.ResolveTrackURL(trackID, quality)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("start hifi adapter: %w", err)
	}

	go http.Serve(listener, mux)

	addr := fmt.Sprintf("http://127.0.0.1:%d", listener.Addr().(*net.TCPAddr).Port)
	return addr, nil
}

// resolveTidalAPIURL returns the first working Tidal API URL from the
// primary + fallback list. Results are cached for 5 minutes to avoid
// health-checking on every download. Returns empty string if none work
// (Spotiflac falls back to community tier).
func (c *Client) resolveTidalAPIURL() string {
	// If no fallbacks configured, just use the primary URL.
	if len(c.tidalAPIFallbacks) == 0 {
		return c.tidalAPIURL
	}

	// Use cached result if fresh.
	if c.resolvedTidalAPI != "" && time.Since(c.resolvedTidalCheck) < 5*time.Minute {
		return c.resolvedTidalAPI
	}

	// Build candidate list: primary first, then fallbacks.
	candidates := []string{}
	if c.tidalAPIURL != "" {
		candidates = append(candidates, c.tidalAPIURL)
	}
	candidates = append(candidates, c.tidalAPIFallbacks...)

	client := &http.Client{Timeout: 8 * time.Second}
	for _, u := range candidates {
		req, err := http.NewRequest("GET", u+"/", nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "spotiflac-lidarr-proxy/1.0")
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		resp.Body.Close()
		// Any HTTP response means the proxy is alive.
		c.resolvedTidalAPI = u
		c.resolvedTidalCheck = time.Now()
		return u
	}

	// None worked — return primary (may still work, health check might be flaky).
	return c.tidalAPIURL
}

// SetRelayPort sets the port the proxy server listens on, used to construct
// the SPOTIFLAC_VERIFY_RELAY_URL passed to SpotiFLAC CLI when FSL is configured
// but no explicit verify_relay_url is set.
func (c *Client) SetRelayPort(port int) {
	c.relayPort = port
}

// LookupUpstreamCB returns the upstream_cb URL stored for the given
// verification state parameter. Used by the verify callback handler
// to forward grants back to SpotiFLAC's local callback server.
func (c *Client) LookupUpstreamCB(state string) (string, bool) {
	v, ok := c.verificationStates.Load(state)
	if !ok {
		return "", false
	}
	s, _ := v.(string)
	return s, ok
}

func (c *Client) Download(ctx context.Context, url, outputDir, service, quality string) (<-chan ProgressEvent, <-chan error) {
	if service == "" {
		service = c.defaultService
	}
	if quality == "" {
		quality = c.defaultQuality
	}

	events := make(chan ProgressEvent, 32)
	errs := make(chan error, 1)

	go func() {
		defer func() {
			close(events)
			close(errs)
		}()

		ctx, cancel := context.WithTimeout(ctx, c.timeout)
		defer cancel()

		// Map proxy quality names to SpotiFLAC CLI uppercase flags
		cliQuality := config.SpotiflacQuality(quality)

		args := []string{
			"--url", url,
			"--output-dir", outputDir,
			"--service", service,
			"--quality", cliQuality,
		}
		tidalURL := c.resolveTidalAPIURL()
		if tidalURL != "" {
			// If the resolved URL is a hifi-api instance (manifest format),
			// start a local adapter that translates to SpotiFLAC format.
			if isHiFiAPI(tidalURL) {
				adapterAddr, err := c.startHiFiAdapter(tidalURL)
				if err == nil {
					tidalURL = adapterAddr
				}
			}
			args = append(args, "--tidal-api-url", tidalURL)
		}
		if c.qobuzAPIURL != "" {
			args = append(args, "--qobuz-api-url", c.qobuzAPIURL)
		}
		cmd := exec.CommandContext(ctx, c.cliPath, args...)

		// Strip proxy env vars from SpotiFLAC subprocess — Go's HTTP client
		// handles HTTP_PROXY differently than curl, causing "server gave HTTP
		// response to HTTPS client" errors through gluetun's proxy.
		// SpotiFLAC connects to public Spotify/Tidal APIs directly.
		cmd.Env = filterOut(os.Environ(),
			"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy",
			"NO_PROXY", "no_proxy")

		// Determine SPOTIFLAC_VERIFY_RELAY_URL:
		// 1. Explicit verify_relay_url config takes priority (user-set)
		// 2. FSL (Byparr/FlareSolverr) auto-construction as fallback
		relayURL := c.verifyRelayURL
		if relayURL == "" && c.fslURL != "" && c.relayPort > 0 {
			addr := c.relayAddress
			if addr == "" {
				addr = autoDetectIP()
			}
			if addr != "" {
				relayURL = fmt.Sprintf("http://%s:%d/api/verify-relay", addr, c.relayPort)
			}
		}
		if relayURL != "" {
			cmd.Env = append(cmd.Env, "SPOTIFLAC_VERIFY_RELAY_URL="+relayURL)
		}

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			errs <- fmt.Errorf("stdout pipe: %w", err)
			return
		}

		if err := cmd.Start(); err != nil {
			errs <- fmt.Errorf("start spotiflac: %w", err)
			return
		}

		var outputBuf bytes.Buffer
		tee := io.TeeReader(stdout, &outputBuf)
		parseProgress(tee, events, errs, &outputBuf, func(ev ProgressEvent) {
			// FSL auto-solving: when Byparr/FlareSolverr is configured and a
			// verification_required event arrives, send the challenge URL to
			// Byparr's headless browser for Turnstile solving.
			if c.fslURL != "" && ev.URL != "" {
				c.solveVerification(ev.URL)
			}
		})

		if err := cmd.Wait(); err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				errs <- fmt.Errorf("spotiflac timed out after %s", c.timeout)
			} else {
				errs <- fmt.Errorf("spotiflac exited: %w", err)
			}
		}
	}()

	return events, errs
}

func (c *Client) SearchMetadata(ctx context.Context, query string) ([]MetadataResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, c.cliPath,
		"--search", query,
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start spotiflac search: %w", err)
	}

	var results []MetadataResult
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		var raw struct {
			Type       string `json:"type"`
			Name       string `json:"name"`
			Artist     string `json:"artist"`
			Album      string `json:"album"`
			SpotifyURL string `json:"spotify_url"`
			CoverURL   string `json:"cover_url"`
			Year       string `json:"year"`
			TrackCount int    `json:"track_count"`
			Title      string `json:"title"`
			ISRC       string `json:"isrc"`
			Genre      string `json:"genre"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &raw); err != nil {
			continue
		}
		url := raw.SpotifyURL
		if url == "" {
			continue
		}
		title := raw.Title
		if title == "" {
			title = raw.Name
		}
		artist := raw.Artist
		if artist == "" {
			artist = raw.Name
		}
		results = append(results, MetadataResult{
			Artist:     artist,
			Album:      raw.Album,
			Title:      title,
			SpotifyURL: url,
			CoverURL:   raw.CoverURL,
			ISRC:       raw.ISRC,
			Genre:      raw.Genre,
			TrackCount: raw.TrackCount,
		})
	}

	if err := cmd.Wait(); err != nil {
		return results, fmt.Errorf("spotiflac search exited: %w", err)
	}

	return results, nil
}

// solveVerification sends a community verification challenge URL to Byparr/FlareSolverr.
// SpotiFLAC's relay mechanism embeds upstream_cb inside the cb= query parameter:
//
//	challenge URL:  https://verify.xx/challenge?cb=<relay-cb-url-with-upstream_cb>&id=...
//	cb value:       http://relay:port/api/verify-relay?upstream_cb=http://127.0.0.1:PORT/session-grant?state=...
//
// Byparr's browser loads the challenge, solves Turnstile, and the page redirects to
// cb?upstream_cb=...&grant=... — the upstream_cb is already in the redirect URL,
// so our /api/verify-relay handler reads it directly from query params.
func (c *Client) solveVerification(challengeURL string) {
	parsed, err := url.Parse(challengeURL)
	if err != nil {
		return
	}

	// upstream_cb is nested inside the cb= query parameter value.
	// Parse cb to extract it for state mapping (used by LookupUpstreamCB
	// if needed, though the callback URL carries upstream_cb directly).
	cbStr := parsed.Query().Get("cb")
	if cbStr == "" {
		return
	}
	cbURL, err := url.Parse(cbStr)
	if err != nil {
		return
	}
	upstreamCB := cbURL.Query().Get("upstream_cb")

	// Track state→upstream_cb for observability (callback carries upstream_cb
	// directly, so the handler doesn't strictly need this lookup).
	var verifyState string
	if upstreamCB != "" {
		if upURL, err := url.Parse(upstreamCB); err == nil {
			verifyState = upURL.Query().Get("state")
			if verifyState != "" {
				c.verificationStates.Store(verifyState, upstreamCB)
			}
		}
	}

	// Send to Byparr/FlareSolverr asynchronously — the browser
	// loads the challenge URL, solves Turnstile, and the redirect
	// hits our verify callback endpoint.
	go func() {
		if err := fslRequest(c.fslURL, challengeURL, c.timeout); err != nil {
			if verifyState != "" {
				c.verificationStates.Delete(verifyState)
			}
		}
	}()
}

// autoDetectIP returns the IP of the default route interface.
// Used when SPOTIFLAC_ADDRESS is not explicitly set.
func autoDetectIP() string {
	addrs, err := os.ReadFile("/proc/net/fib_trie")
	if err != nil {
		return ""
	}
	for _, prefix := range []string{"172.", "10.", "192.168."} {
		if idx := strings.Index(string(addrs), prefix); idx >= 0 {
			end := idx
			for end < len(addrs) && (addrs[end] >= '0' && addrs[end] <= '9' || addrs[end] == '.') {
				end++
			}
			ip := string(addrs[idx:end])
			if len(ip) >= 7 {
				return ip
			}
		}
	}
	return ""
}

// filterOut returns a copy of env without entries whose key (before '=')
// matches any of the given names (case-insensitive).
func filterOut(env []string, names ...string) []string {
	drop := make(map[string]bool, len(names))
	for _, n := range names {
		drop[strings.ToUpper(n)] = true
	}
	out := make([]string, 0, len(env))
	for _, e := range env {
		key := strings.ToUpper(e[:strings.IndexByte(e, '=')])
		if !drop[key] {
			out = append(out, e)
		}
	}
	return out
}

// fslRequest sends a URL to a Byparr/FlareSolverr-compatible API for
// headless browser rendering (Turnstile solving).
func fslRequest(fslBase, targetURL string, timeout time.Duration) error {
	payload := map[string]interface{}{
		"url":         targetURL,
		"max_timeout": int(timeout.Seconds()),
	}
	body, _ := json.Marshal(payload)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", fslBase+"/v1", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("fsl request build: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("fsl request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("fsl returned HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}
