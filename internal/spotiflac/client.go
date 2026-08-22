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
	"path/filepath"
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

	// pythonBudget bounds the embedded-Python-backend phase of one attempt.
	// Zero means "same as timeout" (legacy behavior). The CLI phase always
	// gets its own full timeout under the job context - the two phases must
	// never share one deadline, or a Python run stuck on a dead service eats
	// the whole budget and the CLI starts into an already-dead context.
	pythonBudget time.Duration

	// skipPythonWithSession gates the session-aware Python-phase skip in
	// Download(): when true and a valid CLI community session exists, jobs
	// whose service the CLI implements go straight to the CLI backends.
	skipPythonWithSession bool

	// log records what the backends did. Without it a Python-backend
	// failure was invisible: CollectPythonResult drops the error on the
	// floor so the job can fall through to the CLI, which is correct, but
	// it also meant the only way to find out why backend 1 failed was to
	// run the wrapper by hand.
	log zerolog.Logger

	// activeCmds maps outputDir -> the backend subprocess currently running
	// for that job dir (CLI or Python wrapper). AbortActive uses it to kill
	// a backend that outlived its terminal error event - which it does:
	// the handler returns on the first error line while the process keeps
	// waiting on a verification callback or finishing remaining tracks.
	activeCmds sync.Map

	// abortedActive records AbortActive kills, for tests.
	abortedMu     sync.Mutex
	abortedActive []string
}

// SetLogger attaches a logger. Optional: the zero value discards.
func (c *Client) SetLogger(log zerolog.Logger) {
	c.log = log
}

// SetPythonBudget caps how long one attempt may spend in the embedded
// Python backend before falling through to the CLI backends. Clamped to the
// CLI timeout at use time. Optional: unset keeps both phases equal to the
// CLI timeout.
func (c *Client) SetPythonBudget(d time.Duration) {
	c.pythonBudget = d
}

// SetSkipPythonWhenSessionPresent enables the session-aware Python-phase
// skip: while the CLI's community session store holds a session that has
// not expired, download attempts for CLI-implemented services (tidal,
// qobuz, amazon) skip the embedded Python backend and go straight to the
// CLI backends. The Python extensions authenticate with their own zarz
// mobile sessions against the same spotbye infrastructure the desktop
// session signs for, so with a valid session they only burn wall-clock time
// (measured 2026-08-22: ~10 min per job during an upstream outage). Deezer
// primaries are exempt: the Python backend is the only one that implements
// deezer. Enabled by default; set false for the legacy always-try-Python
// order.
func (c *Client) SetSkipPythonWhenSessionPresent(b bool) {
	c.skipPythonWithSession = b
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

// trackProbeID is a sentinel Tidal track id used to verify that a candidate
// API can actually resolve tracks. The root response proves only that the
// process is up: monochrome-api.samidy.com answered 200 {"version":"2.3"}
// while every /track/ call failed with "Token refresh failed: 403 from
// auth.tidal.com" (Tidal blocking the instance's OAuth credentials, observed
// 2026-08-20..21). Handing such an instance to spotiflac-cli as
// --tidal-api-url turns every track into a hard failure, after which the CLI
// falls through to the community tier and burns minutes on verification.
const trackProbeID = "1"

// probeHiFiTrack verifies that a hifi-api candidate whose root looks alive
// can actually serve a track request. Only non-2xx responses other than 404
// mark it dead: a 404 means the credentials work and the sentinel simply does
// not exist, and a 202 queue ticket means the instance is busy but functional.
// LOW quality keeps the probe cheap in the unlikely case the sentinel exists.
func probeHiFiTrack(baseURL string) (bool, string) {
	req, err := http.NewRequest("GET", baseURL+"/track/?id="+trackProbeID+"&quality=LOW", nil)
	if err != nil {
		return false, "build probe request: " + err.Error()
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "spotiflac-lidarr-proxy/1.0")

	resp, err := (&http.Client{Timeout: probeTimeout}).Do(req)
	if err != nil {
		return false, err.Error()
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return true, "" // manifest or queued ticket
	case resp.StatusCode == http.StatusNotFound:
		return true, "" // auth worked; the sentinel id is simply unknown
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return false, fmt.Sprintf("HTTP %d: %s", resp.StatusCode, snippet(body))
	}
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
	// still classify it so the hifi-adapter gets wired up when needed, and
	// verify it can actually resolve tracks (a hifi root answers 200 even
	// while its Tidal OAuth is broken - see probeHiFiTrack).
	if len(c.tidalAPIFallbacks) == 0 {
		if c.tidalAPIURL == "" {
			return "", apiDead
		}
		kind := probeAPI(c.tidalAPIURL)
		if kind == apiHiFi {
			if ok, reason := probeHiFiTrack(c.tidalAPIURL); !ok {
				c.log.Warn().Str("url", c.tidalAPIURL).Str("reason", reason).
					Msg("tidal API root answers but cannot resolve tracks; treating as dead")
				return "", apiDead
			}
		}
		return c.tidalAPIURL, kind
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
		kind := probeAPI(u)
		if kind == apiDead {
			continue
		}
		// A live root is not a working API: hifi instances keep answering /
		// with their version banner while Tidal blocks their OAuth token,
		// and every real track then fails. Verify the track path before
		// handing the candidate to spotiflac-cli.
		if kind == apiHiFi {
			if ok, reason := probeHiFiTrack(u); !ok {
				c.log.Warn().Str("url", u).Str("reason", reason).
					Msg("tidal API candidate root answers but cannot resolve tracks; skipping")
				continue
			}
		}
		c.resolvedTidalAPI, c.resolvedTidalKind = u, kind
		break
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

// AbortActive terminates the backend subprocess registered for outputDir,
// if one is still running. Called after a FAILED attempt: the backend can
// outlive its terminal error event (waiting on a verification callback,
// processing remaining tracks), and a lingering process races the next
// attempt - same job dir, shared bolt state files (the "ISRC cache:
// timeout" warnings observed live 2026-08-21 came from exactly this
// overlap). No-op when nothing is registered; success paths never abort,
// so a finishing backend is never cut off mid-flush.
func (c *Client) AbortActive(outputDir string) {
	v, ok := c.activeCmds.Load(outputDir)
	if !ok {
		return
	}
	cmd, _ := v.(*exec.Cmd)
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	c.abortedMu.Lock()
	c.abortedActive = append(c.abortedActive, outputDir)
	c.abortedMu.Unlock()
}

// AbortedActiveForTest reports whether AbortActive killed a backend for
// outputDir. Exported for the handler tests.
func (c *Client) AbortedActiveForTest(outputDir string) bool {
	c.abortedMu.Lock()
	defer c.abortedMu.Unlock()
	for _, d := range c.abortedActive {
		if d == outputDir {
			return true
		}
	}
	return false
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

		// Backend priority:
		//   1. Python wrapper (embedded) — multi-service fallback, no captcha
		//   2. CLI with custom API URL + hifi-adapter — bypasses community tier
		//   3. CLI with FSL/Byparr auto-solve — headless captcha solving
		//   4. CLI community tier (manual/relay verification)
		//
		// Phases 1 and 2-4 get SEPARATE deadlines under the caller's context.
		// They used to share one WithTimeout(c.timeout): a Python run stuck on
		// a dead service consumed the entire budget, and the CLI phase then
		// failed instantly with "start spotiflac: context deadline exceeded" -
		// the fallback the whole cascade exists for could never start during
		// exactly the outages it is meant to ride out.
		pyBudget := c.pythonBudget
		if pyBudget <= 0 || pyBudget > c.timeout {
			pyBudget = c.timeout
		}
		pyCtx, pyCancel := context.WithTimeout(ctx, pyBudget)
		defer pyCancel()

		// Backend 1: Try Python wrapper first. On any failure (no Python,
		// no module, download error) fall through to CLI.
		pythonOK := false
		pythonBin := findPython(c.pythonVenv)
		wrapperPath, wrapErr := extractPythonWrapper()
		if wrapErr == nil {
			if _, statErr := os.Stat(pythonBin); statErr == nil {
				if skip, reason := c.skipPythonPhase(service); skip {
					// A valid CLI community session means the CLI tier needs no
					// verification at all, and the Python extensions (zarz mobile
					// sessions, same spotbye infrastructure) can only repeat the
					// outcome with a multi-minute provider-budget burn attached.
					c.log.Info().Str("service", service).Str("reason", reason).
						Msg("skipping python backend, going straight to CLI")
				} else {
					pythonOK = true
					pyEvents, pyErrs := c.downloadWithPython(pyCtx, pythonBin, wrapperPath, url, outputDir, service, quality)
					if c.CollectPythonResult(pyEvents, pyErrs, events, errs) {
						return // Python succeeded
					}
					// Python failed — fall through to CLI
				}
			}
		}

		// Backends 2-4: SpotiFLAC CLI, with its own fresh budget.
		cliCtx, cliCancel := context.WithTimeout(ctx, c.timeout)
		defer cliCancel()
		c.runCLIBackend(cliCtx, events, errs, url, outputDir, service, quality, pythonOK)
	}()

	return events, errs
}

// DownloadCLI runs only the SpotiFLAC CLI backends (custom API URL, FSL
// auto-solve, community tier) - never the embedded Python wrapper.
//
// The handler's per-service fallback loop uses this when the Python backend
// is available: the wrapper's internal cascade has already tried every
// configured service for this release, so re-running it once per fallback
// service would just repeat the same failures and burn the job's wall-clock
// budget. The CLI is a genuinely different code path (custom Tidal/Qobuz API
// URLs, hifi adapter, FSL Turnstile solving), which is what the per-service
// fallback exists to try.
func (c *Client) DownloadCLI(ctx context.Context, url, outputDir, service, quality string) (<-chan ProgressEvent, <-chan error) {
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

		cliCtx, cliCancel := context.WithTimeout(ctx, c.timeout)
		defer cliCancel()
		c.runCLIBackend(cliCtx, events, errs, url, outputDir, service, quality, false)
	}()

	return events, errs
}

// HasPythonBackend reports whether the embedded Python wrapper can run at
// all: a usable interpreter AND an extractable wrapper script.
func (c *Client) HasPythonBackend() bool {
	if _, err := extractPythonWrapper(); err != nil {
		return false
	}
	bin := findPython(c.pythonVenv)
	_, err := os.Stat(bin)
	return err == nil
}

// runCLIBackend executes backends 2-4 (the SpotiFLAC CLI) writing to the
// given channels. ctx must carry the phase's own deadline; the caller owns
// closing the channels.
//
// sawPython tells the failure message that the Python backend already ran
// and failed for this service - without it a deezer job reads as if the
// proxy had never tried its preferred backend at all.
//
//nolint:gocyclo // The CLI backend has one branch per failure mode; splitting it would obscure the cascade.
func (c *Client) runCLIBackend(ctx context.Context, events chan<- ProgressEvent, errs chan<- error, url, outputDir, service, quality string, sawPython bool) {
	// Not every service the category vocabulary accepts exists here.
	// spotiflac-cli implements tidal, qobuz and amazon; Deezer lives only
	// in the Python backend's extensions. Handing it "deezer" produces
	//
	//	{"message":"track scared: Unknown service: deezer","type":"error"}
	//
	// immediately followed by a "complete" event for the same track, so
	// the failure is easy to mistake for a success. Say what actually
	// happened instead of running a command that cannot work.
	if !cliSupportsService(service) {
		errMsg := fmt.Errorf(
			"service %q is only available through the Python backend, and that backend failed; spotiflac-cli supports %s",
			service, strings.Join(cliServices, ", "))
		if !sawPython {
			errMsg = fmt.Errorf(
				"service %q is only available through the Python backend, which is not available in this deployment; spotiflac-cli supports %s",
				service, strings.Join(cliServices, ", "))
		}
		errs <- errMsg
		return
	}

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
	c.activeCmds.Store(outputDir, cmd)
	defer c.activeCmds.Delete(outputDir)

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
	relayURL := resolveRelayURL(c.verifyRelayURL, c.fslURL, c.relayAddress, autoDetectIP(), c.relayPort)
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
		c.activeCmds.Store(outputDir, cmd)
		defer c.activeCmds.Delete(outputDir)
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
	// Both channels are drained until both are closed, rather than returning
	// the moment pyEvents closes. select chooses at random among ready cases
	// and a closed channel is always ready, so returning on the first closed
	// pyEvents dropped whatever was still sitting in pyErrs - the backend's
	// own failure reason, about half the time, at random.
	for pyEvents != nil || pyErrs != nil {
		select {
		case evt, ok := <-pyEvents:
			if !ok {
				pyEvents = nil
				continue
			}
			if evt.Type == "complete" {
				sawComplete = true
			}
			if sawComplete {
				mainEvents <- evt
			}
		case e, ok := <-pyErrs:
			if !ok {
				pyErrs = nil
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
				Str("detail", significantLines(detail, 12)).
				Msg("python backend failed, falling through to CLI")
		}
	}
	return sawComplete
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

// cliServices are the services spotiflac-cli implements. Deezer is absent on
// purpose: it exists only as a Python-backend extension.
var cliServices = []string{"tidal", "qobuz", "amazon"}

// cliSupportsService reports whether spotiflac-cli can handle this service.
func cliSupportsService(service string) bool {
	for _, s := range cliServices {
		if strings.EqualFold(service, s) {
			return true
		}
	}
	return false
}

// communitySessionFile returns the path of the CLI's community session store.
// The CLI writes it after exchanging a verification grant; while its
// expires_at is in the future the CLI community tier downloads without any
// verification at all. SPF_COMMUNITY_SESSION_FILE overrides the default
// ($HOME/.spotiflac/community_session.json) - tests use it.
func communitySessionFile() string {
	if p := os.Getenv("SPF_COMMUNITY_SESSION_FILE"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".spotiflac", "community_session.json")
}

// sessionSkew is how far before its nominal expiry a community session stops
// counting as valid: HMAC windows roll every 300 s and a session that dies
// mid-download just turns a healthy grab into a re-verification race.
const sessionSkew = 120 * time.Second

type communitySession struct {
	ExpiresAt string `json:"expires_at"`
}

// communitySessionValid reports whether the CLI's community session store
// holds a session whose expiry is still more than sessionSkew in the future,
// and when that expiry is. A missing or unreadable store is NOT an error:
// no session is the normal state on a fresh container.
func communitySessionValid() (bool, time.Time) {
	path := communitySessionFile()
	if path == "" {
		return false, time.Time{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, time.Time{}
	}
	var s communitySession
	if err := json.Unmarshal(data, &s); err != nil || s.ExpiresAt == "" {
		return false, time.Time{}
	}
	exp, err := time.Parse(time.RFC3339, s.ExpiresAt)
	if err != nil {
		return false, time.Time{}
	}
	return time.Now().Add(sessionSkew).Before(exp), exp
}

// skipPythonPhase reports whether the embedded-Python phase should be skipped
// for this service because a valid CLI community session makes it dead
// weight (see SetSkipPythonWhenSessionPresent). The second return is the
// reason for logs and tests; empty when the Python phase runs as usual.
func (c *Client) skipPythonPhase(service string) (bool, string) {
	if !c.skipPythonWithSession {
		return false, ""
	}
	if !cliSupportsService(service) {
		return false, fmt.Sprintf("service %q is Python-only", service)
	}
	valid, exp := communitySessionValid()
	if !valid {
		return false, "no valid CLI community session"
	}
	return true, "valid CLI community session until " + exp.Format(time.RFC3339)
}

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

// resolveRelayURL determines the SPOTIFLAC_VERIFY_RELAY_URL handed to the
// CLI so an external solver (FSL/trawl) can deliver a completed verification
// grant back to us. An explicit user-set URL always wins; otherwise one is
// constructed from the configured address when FSL is enabled.
//
// One exception where NO relay is built at all: an FSL reachable only via
// loopback lives in our own network namespace, so its browser reaches the
// CLI's own callback listener directly. Setting a relay would rewrite the
// challenge's cb= to a routable address, which spotbye's verify server
// rejects (it validates cb as loopback-only - measured 2026-08-21: a
// bridge-IP cb loads a 400 SPA error page, so the solver solves nothing).
// In that case the CLI must keep its own loopback cb.
//
// The address must be reachable FROM the solver's container. A loopback
// address (SPOTIFLAC_ADDRESS=127.0.0.1, the common case) only exists inside
// our own network namespace: the solver solves the challenge, follows the
// cb= redirect into its own empty loopback, and the grant is silently lost -
// the CLI then reports "verification timed out" while trawl's logs show
// "cf_clearance obtained". Measured live on 2026-08-21. When the configured
// address is loopback (or unset) we therefore prefer the detected LAN
// address, which is routable between containers on the compose network.
func resolveRelayURL(explicit, fslURL, configuredAddr, detectedAddr string, port int) string {
	if explicit != "" {
		return explicit
	}
	if fslURL == "" || port <= 0 {
		return ""
	}
	if fslSharesOurNetns(fslURL) {
		return ""
	}
	addr := configuredAddr
	if addr == "" || isLoopback(addr) {
		if detectedAddr != "" {
			addr = detectedAddr
		}
	}
	if addr == "" {
		return ""
	}
	return fmt.Sprintf("http://%s:%d/api/verify-relay", addr, port)
}

// isLoopback reports whether addr is a loopback literal. Only exact literals
// are treated as such: a user who explicitly configured a non-loopback
// address gets it verbatim.
func isLoopback(addr string) bool {
	return addr == "127.0.0.1" || addr == "::1" || strings.HasPrefix(addr, "127.")
}

// fslSharesOurNetns reports whether the FSL URL points at loopback, i.e.
// whether the solver runs in the same network namespace as us. Such a
// solver's browser can reach our loopback listeners directly, so no relay
// hop is needed - and for spotbye a relay hop is actively harmful (see
// resolveRelayURL).
func fslSharesOurNetns(fslURL string) bool {
	u, err := url.Parse(fslURL)
	if err != nil || u.Host == "" {
		return false
	}
	host := u.Hostname()
	return host == "localhost" || isLoopback(host)
}

// autoDetectIP returns the IP of the default route interface.
// Used when SPOTIFLAC_ADDRESS is not explicitly set.
// autoDetectIP returns the best-guess locally-routable private IP of this
// network namespace. Preference order:
//
//  1. 172.16.0.0/12 - Docker's default bridge space. In the potatostack
//     deployment the proxy shares gluetun's netns, whose eth0 carries the
//     compose-bridge address (172.22.x) on exactly this range - and that is
//     the interface sibling containers (trawl/FSL) can reach, which is all
//     the verify relay needs.
//  2. any other RFC1918 address as a last resort.
//
// Loopback and non-private addresses are excluded. The previous fib-trie
// string scan returned the FIRST "172." occurrence anywhere in the file,
// which in practice was a route prefix (172.16.0.0) rather than a local IP.
func autoDetectIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	fallback := ""
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipNet.IP
		if ip.IsLoopback() || !ip.IsPrivate() {
			continue
		}
		if inDockerBridgeSpace(ip) {
			return ip.String()
		}
		if fallback == "" {
			fallback = ip.String()
		}
	}
	return fallback
}

// inDockerBridgeSpace reports whether ip falls in 172.16.0.0/12.
func inDockerBridgeSpace(ip net.IP) bool {
	v4 := ip.To4()
	return v4 != nil && v4[0] == 172 && v4[1] >= 16 && v4[1] <= 31
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
