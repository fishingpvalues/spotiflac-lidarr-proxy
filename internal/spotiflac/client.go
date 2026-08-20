package spotiflac

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"

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

	// pythonVenv is the path to a Python venv binary (e.g. /venv/bin/python3).
	// When set, the proxy tries the Python wrapper first (embedded),
	// falling back to CLI if Python fails. Auto-detected if empty.
	pythonVenv string

	// tidalAPIFallbacks is a list of additional Tidal API proxy URLs
	// tried in order when the primary tidalAPIURL fails.
	tidalAPIFallbacks []string

	// resolvedTidalAPI caches the last known working Tidal API URL, its
	// classification, and when the probe ran. Guarded by tidalMu: Download()
	// runs one goroutine per concurrent job and all of them consult this.
	// A zero resolvedTidalCheck means "never probed"; an empty
	// resolvedTidalAPI with a non-zero check time means "probed, all dead".
	tidalMu            sync.Mutex
	resolvedTidalAPI   string
	resolvedTidalKind  apiKind
	resolvedTidalCheck time.Time

	// verificationStates maps state param → upstream_cb URL for
	// community verification relay forwarding (FSL/Byparr path).
	verificationStates sync.Map

	// fallbackServices is the ordered list of fallback service names
	// (e.g. ["qobuz", "deezer"]) used by the Python wrapper's internal
	// cascade and the Go-level fallback chain.
	fallbackServices []string

	// log records what the backends did. Without it a Python-backend
	// failure was invisible: CollectPythonResult drops the error on the
	// floor so the job can fall through to the CLI, which is correct, but
	// it also meant the only way to find out why backend 1 failed was to
	// run the wrapper by hand.
	log zerolog.Logger
}

// SetLogger attaches a logger. Optional: the zero value discards.
func (c *Client) SetLogger(log zerolog.Logger) {
	c.log = log
}

func NewClient(cliPath string, timeout time.Duration, defaultService, defaultQuality, verifyRelayURL, tidalAPIURL, qobuzAPIURL string, tidalAPIFallbacks []string, pythonVenv string, fallbackServices []string) *Client {
	fslURL := os.Getenv("SPOTIFLAC_FSL_URL")
	relayAddress := os.Getenv("SPOTIFLAC_ADDRESS")

	return &Client{
		cliPath:           cliPath,
		timeout:           timeout,
		defaultService:    defaultService,
		defaultQuality:    defaultQuality,
		verifyRelayURL:    verifyRelayURL,
		tidalAPIURL:       tidalAPIURL,
		qobuzAPIURL:       qobuzAPIURL,
		fslURL:            fslURL,
		relayAddress:      relayAddress,
		tidalAPIFallbacks: tidalAPIFallbacks,
		pythonVenv:        pythonVenv,
		fallbackServices:  fallbackServices,
	}
}

// probeTimeout bounds every Tidal-API health probe. http.DefaultClient has
// no timeout at all, so a candidate that accepts the connection and then
// stalls used to hang the download goroutine indefinitely.
const probeTimeout = 8 * time.Second

// apiKind classifies what, if anything, a candidate Tidal API URL is.
type apiKind int

const (
	apiDead      apiKind = iota // unreachable, non-2xx, or not an API at all
	apiSpotiFLAC                // SpotiFLAC-compatible: /track/ returns {"url": "..."}
	apiHiFi                     // hifi-api: /track/ returns a base64 manifest
)

// probeAPI classifies a candidate Tidal API base URL by its root response.
//
// The old check was "any HTTP response means the proxy is alive", which is
// wrong in both directions and was breaking downloads in production:
//
//   - https://lossless.wtf and https://monochrome.samidy.com are the
//     Monochrome *web UI*, not its API. They answer 200 with HTML, so they
//     were accepted, cached for 5 minutes, and handed to spotiflac-cli as
//     --tidal-api-url, where every track lookup then failed.
//   - https://api.monochrome.tf answers 503 and
//     https://arran.monochrome.tf answers 502. Both counted as "alive" and,
//     being first in the list, shadowed the one instance that does work.
//
// A real API answers 2xx with a JSON body, so that is what we require. A
// hifi-api additionally identifies itself as {"version":"2.x","Repo":"..."},
// which is what tells us to put the manifest-translating adapter in front.
func probeAPI(baseURL string) apiKind {
	req, err := http.NewRequest("GET", baseURL+"/", nil)
	if err != nil {
		return apiDead
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "spotiflac-lidarr-proxy/1.0")

	resp, err := (&http.Client{Timeout: probeTimeout}).Do(req)
	if err != nil {
		return apiDead
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return apiDead
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return apiDead
	}

	var check struct {
		Version string `json:"version"`
		Repo    string `json:"Repo"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(body), &check); err != nil {
		return apiDead // HTML page, Cloudflare interstitial, plain text, ...
	}
	if check.Version != "" && check.Repo != "" {
		return apiHiFi
	}
	return apiSpotiFLAC
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
		if err := json.NewEncoder(w).Encode(result); err != nil {
			_ = err // client likely disconnected
		}
	})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("start hifi adapter: %w", err)
	}

	go func() {
		if err := http.Serve(listener, mux); err != nil && err != http.ErrServerClosed {
			_ = err // non-fatal, adapter is best-effort
		}
	}()

	addr := fmt.Sprintf("http://127.0.0.1:%d", listener.Addr().(*net.TCPAddr).Port)
	return addr, nil
}

// resolveTidalAPIURL returns the first working Tidal API URL from the
// primary + fallback list, together with what kind of API it is. Results are
// cached for 5 minutes to avoid health-checking on every download. Returns
// an empty URL and apiDead if none work, in which case the caller omits
// --tidal-api-url entirely and SpotiFLAC falls back to the community tier.
//
// Downloads run concurrently (SPF_MAX_CONCURRENT), so the cache is guarded:
// the fields were previously read and written from several goroutines at
// once with no synchronization.
func (c *Client) resolveTidalAPIURL() (string, apiKind) {
	// If no fallbacks are configured, the single explicit URL is the
	// operator's deliberate choice - use it without second-guessing, but
	// still classify it so the hifi-adapter gets wired up when needed.
	if len(c.tidalAPIFallbacks) == 0 {
		if c.tidalAPIURL == "" {
			return "", apiDead
		}
		return c.tidalAPIURL, probeAPI(c.tidalAPIURL)
	}

	c.tidalMu.Lock()
	defer c.tidalMu.Unlock()

	// Use the cached verdict if fresh. Failure is cached too: with a list of
	// mostly-dead public mirrors, an uncached miss re-probed every candidate
	// on every single download, stalling each job by up to
	// len(candidates) * probeTimeout before it even started.
	if time.Since(c.resolvedTidalCheck) < 5*time.Minute && !c.resolvedTidalCheck.IsZero() {
		return c.resolvedTidalAPI, c.resolvedTidalKind
	}

	// Build candidate list: primary first, then fallbacks.
	candidates := []string{}
	if c.tidalAPIURL != "" {
		candidates = append(candidates, c.tidalAPIURL)
	}
	candidates = append(candidates, c.tidalAPIFallbacks...)

	c.resolvedTidalAPI, c.resolvedTidalKind = "", apiDead
	for _, u := range candidates {
		if kind := probeAPI(u); kind != apiDead {
			c.resolvedTidalAPI, c.resolvedTidalKind = u, kind
			break
		}
	}
	c.resolvedTidalCheck = time.Now()

	return c.resolvedTidalAPI, c.resolvedTidalKind
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

//nolint:gocyclo // Fallback cascade (Python→CLI→FSL→community) is inherently branched.
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

		// Backend priority:
		//   1. Python wrapper (embedded) — multi-service fallback, no captcha
		//   2. CLI with custom API URL + hifi-adapter — bypasses community tier
		//   3. CLI with FSL/Byparr auto-solve — headless captcha solving
		//   4. CLI community tier (manual/relay verification)

		// Backend 1: Try Python wrapper first. On any failure (no Python,
		// no module, download error) fall through to CLI.
		pythonBin := findPython(c.pythonVenv)
		wrapperPath, wrapErr := extractPythonWrapper()
		if wrapErr == nil {
			if _, statErr := os.Stat(pythonBin); statErr == nil {
				pyEvents, pyErrs := c.downloadWithPython(ctx, pythonBin, wrapperPath, url, outputDir, service, quality)
				if c.CollectPythonResult(pyEvents, pyErrs, events, errs) {
					return // Python succeeded
				}
				// Python failed — fall through to CLI
			}
		}

		// Backend 2-4: SpotiFLAC CLI
		cliQuality := config.SpotiflacQuality(quality)

		args := []string{
			"--url", url,
			"--output-dir", outputDir,
			"--service", service,
			"--quality", cliQuality,
		}
		tidalURL, tidalKind := c.resolveTidalAPIURL()
		if tidalURL != "" {
			// A hifi-api instance speaks manifests, not direct URLs, so put
			// the translating adapter in front of it. If the adapter can't
			// start, skip the custom API rather than handing spotiflac-cli a
			// URL whose responses it cannot parse.
			if tidalKind == apiHiFi {
				adapterAddr, err := c.startHiFiAdapter(tidalURL)
				if err != nil {
					tidalURL = ""
				} else {
					tidalURL = adapterAddr
				}
			}
			if tidalURL != "" {
				args = append(args, "--tidal-api-url", tidalURL)
			}
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

		// Without this the CLI's stderr goes to /dev/null (exec.Cmd's
		// default for a nil Stderr), so pydoll's and the extension bridge's
		// diagnostics were discarded before anyone could read them. Only
		// this goroutine reads the buffer, and only after Wait returns.
		var stderrBuf bytes.Buffer
		cmd.Stderr = &stderrBuf

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
				errs <- newExitError("cli exited", err, &stderrBuf, &outputBuf)
			}
		}
	}()

	return events, errs
}

// downloadWithPython runs the embedded SpotiFLAC Python wrapper.
// Returns channels — caller must consume both.
func (c *Client) downloadWithPython(ctx context.Context, pythonBin, wrapperPath, url, outputDir, service, quality string) (<-chan ProgressEvent, <-chan error) {
	events := make(chan ProgressEvent, 32)
	errs := make(chan error, 1)

	go func() {
		defer func() { close(events); close(errs) }()

		// Build service cascade: primary first, then configured fallbacks.
		svcList := service
		for _, fb := range c.fallbackServices {
			if fb != service {
				svcList += "," + fb
			}
		}

		args := []string{
			wrapperPath,
			"--url", url,
			"--output-dir", outputDir,
			"--service", svcList,
			"--quality", quality,
		}

		cmd := exec.CommandContext(ctx, pythonBin, args...)
		cmd.Env = os.Environ() // passes HTTP_PROXY through

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			errs <- fmt.Errorf("python stdout pipe: %w", err)
			return
		}

		var stderrBuf bytes.Buffer
		cmd.Stderr = &stderrBuf

		if err := cmd.Start(); err != nil {
			errs <- fmt.Errorf("start python wrapper: %w", err)
			return
		}

		var outputBuf bytes.Buffer
		tee := io.TeeReader(stdout, &outputBuf)
		parseProgress(tee, events, errs, &outputBuf, nil)

		if err := cmd.Wait(); err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				errs <- fmt.Errorf("python download timed out after %s", c.timeout)
			} else {
				errs <- newExitError("python wrapper exited", err, &stderrBuf, &outputBuf)
			}
		}
	}()

	return events, errs
}

// CollectPythonResult drains Python channels. If a "complete" event arrives,
// it forwards all events+errors to the main channels and returns true.
// Otherwise returns false (CLI fallback).
func (c *Client) CollectPythonResult(pyEvents <-chan ProgressEvent, pyErrs <-chan error, mainEvents chan<- ProgressEvent, mainErrs chan<- error) bool {
	var sawComplete bool
	for {
		select {
		case evt, ok := <-pyEvents:
			if !ok {
				return sawComplete
			}
			if evt.Type == "complete" {
				sawComplete = true
			}
			if sawComplete {
				mainEvents <- evt
			}
		case e, ok := <-pyErrs:
			if !ok {
				continue
			}
			if e == nil {
				continue
			}
			if sawComplete {
				mainErrs <- e
				continue
			}
			// Not forwarded on purpose - without a "complete" the caller
			// falls through to the CLI, and surfacing this error would fail
			// the job instead. It still gets logged: this is the only
			// record of why the backend the proxy tries first did not work.
			var de *DownloadError
			detail := ""
			if errors.As(e, &de) {
				detail = de.RawOutput
			}
			c.log.Warn().
				Err(e).
				Str("detail", lastNBytes([]byte(detail), 2048)).
				Msg("python backend failed, falling through to CLI")
		}
	}
}

// SearchMetadata resolves a free-text query to releases, Python backend
// first and spotiflac-cli second.
//
// The order matters and is not cosmetic. `spotiflac-cli --search` reports
// `"track_count": 0` and `"year": ""` for every single hit, and the Newznab
// indexer derives the release size, the `files` attribute and the release
// year from exactly those three numbers. Lidarr then scores a 0-byte release
// with no year below its own album-match threshold and rejects it - "Album
// match is not close enough: 77.6 % vs 80 % [year, country, tracks]" was on
// every SpotiFLAC grab in production. The bundled Python module has the real
// values (SpotifyMetadataClient), so ask it, and fall back to the CLI only
// when Python is unavailable or answers nothing.

// newExitError reports a non-zero subprocess exit together with what the
// subprocess actually said.
//
// A backend that dies without emitting its own JSON "error" event reached
// Lidarr as nothing but `spotiflac exited: exit status 1`. Both streams are
// needed: SpotiFLAC prints its provider-cascade reasons ("ext:tidal-web:
// NETWORK_ERROR: Timeout (120s) calling download") to stdout, while pydoll
// and the extension bridge log to stderr, and the two failures look nothing
// alike.
func newExitError(context string, err error, stderrBuf, outputBuf *bytes.Buffer) error {
	return &DownloadError{
		Message: fmt.Sprintf("%s: %s", context, err),
		RawOutput: joinNonEmpty(
			significantLines(stderrBuf.String(), 12),
			significantLines(outputBuf.String(), 12),
		),
	}
}

func (c *Client) SearchMetadata(ctx context.Context, query string) ([]MetadataResult, error) {
	pythonBin := findPython(c.pythonVenv)
	if wrapperPath, err := extractPythonWrapper(); err == nil {
		if _, statErr := os.Stat(pythonBin); statErr == nil {
			results, err := c.runSearch(ctx, pythonBin, wrapperPath, "--search", query)
			if err == nil && len(results) > 0 {
				return results, nil
			}
		}
	}
	return c.runSearch(ctx, c.cliPath, "--search", query)
}

// searchTimeout bounds one search invocation. Album enrichment in the Python
// backend adds one Spotify call per album hit, and it carries its own smaller
// budget, so this only has to be generous enough not to cut that short.
const searchTimeout = 60 * time.Second

func (c *Client) runSearch(ctx context.Context, bin string, args ...string) ([]MetadataResult, error) {
	ctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, args...)

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
		result, ok := parseSearchLine(scanner.Bytes())
		if !ok {
			continue
		}
		results = append(results, result)
	}

	if err := cmd.Wait(); err != nil {
		return results, fmt.Errorf("spotiflac search exited: %w", err)
	}

	return results, nil
}

// parseSearchLine turns one JSON line of search output into a result, or
// reports that the line carried no usable release.
func parseSearchLine(line []byte) (MetadataResult, bool) {
	var raw struct {
		Type       string          `json:"type"`
		Entity     string          `json:"entity"`
		Name       string          `json:"name"`
		Artist     string          `json:"artist"`
		Album      string          `json:"album"`
		SpotifyURL string          `json:"spotify_url"`
		CoverURL   string          `json:"cover_url"`
		Year       json.RawMessage `json:"year"`
		TrackCount int             `json:"track_count"`
		Title      string          `json:"title"`
		ISRC       string          `json:"isrc"`
		Genre      string          `json:"genre"`
	}
	if err := json.Unmarshal(line, &raw); err != nil {
		return MetadataResult{}, false
	}
	if raw.SpotifyURL == "" {
		return MetadataResult{}, false
	}

	title := raw.Title
	if title == "" {
		title = raw.Name
	}
	artist := raw.Artist
	if artist == "" {
		artist = raw.Name
	}

	result := MetadataResult{
		Artist:     artist,
		Album:      raw.Album,
		Title:      title,
		SpotifyURL: raw.SpotifyURL,
		CoverURL:   raw.CoverURL,
		ISRC:       raw.ISRC,
		Genre:      raw.Genre,
		Year:       parseYear(raw.Year),
		TrackCount: raw.TrackCount,
		Entity:     raw.Entity,
	}
	// An album hit from the CLI carries its title in `name` and leaves
	// `album` empty. Recover it, or the indexer's "an album must have an
	// album name" rule throws away the only releases Lidarr can import.
	if result.Album == "" && result.EntityKind() == EntityAlbum {
		result.Album = title
	}
	return result, true
}

// parseYear accepts both shapes the two backends emit: the Python wrapper
// sends a number, spotiflac-cli sends a string ("2013", "2013-05-17", or ""
// for unknown). The field was previously read into a local string and then
// never assigned to the result at all, so every release published year 0.
func parseYear(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var asInt int
	if err := json.Unmarshal(raw, &asInt); err == nil {
		return asInt
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err != nil {
		return 0
	}
	if len(asString) < 4 {
		return 0
	}
	year, err := strconv.Atoi(asString[:4])
	if err != nil {
		return 0
	}
	return year
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
