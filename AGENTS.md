# AGENTS.md — Spotiflac-Lidarr Proxy

Instructions for LLM agents working in this repository.

## Project Context

Go service bridging Lidarr ↔ SpotiFLAC. Implements **SABnzbd download client API** and **Newznab indexer API** so Lidarr treats this proxy as a standard Usenet downloader. Shells out to a headless SpotiFLAC CLI to download FLAC from Tidal/Qobuz/Amazon/Deezer.

**Module:** `github.com/fishingpvalues/spotiflac-lidarr-proxy`  
**Go version:** 1.25+  
**SpotiFLAC fork:** `github.com/fishingpvalues/SpotiFLAC`

## Architecture

```
cmd/server/          Cobra+fiber HTTP server entry point
internal/api/        Middleware (auth, logging)
internal/api/sabnzbd/ SABnzbd API handlers (Lidarr download client)
internal/api/newznab/ Newznab API handlers (Lidarr indexer)
internal/spotiflac/  SpotiFLAC CLI + Python wrapper subprocess manager
  python_wrapper/    Embedded Python script (extracted at runtime)
internal/queue/      SQLite-backed job queue (modernc.org/sqlite)
internal/indexer/    Spotify metadata → Newznab XML
internal/storage/    File system operations
internal/config/     Viper config (env vars prefixed SPF_)
pkg/sabnzbd/         Shared SABnzbd JSON types
tests/integration/   Integration tests (docker-compose)
```

### Search (Client.SearchMetadata)

Python backend first, `spotiflac-cli` second, same cascade shape as downloads.
The order is load-bearing: `spotiflac-cli --search` reports `track_count: 0`
and `year: ""` for every hit, and `internal/indexer` derives the release size,
the `files` attr and the release year from exactly those. Lidarr scores a
0-byte release from year 0 below its album-match threshold and rejects it. The
Python wrapper's `--search` mode asks `SpotifyMetadataClient` instead, and
enriches album hits with one extra call each under a time budget.

`parseSearchLine` accepts both shapes the backends emit - the wrapper sends
`year` as a number, the CLI as a string - see `parseYear`.

### Download cascade (Client.Download)

The proxy tries backends in priority order, first success wins:

1. **Python wrapper (embedded)** — Extracted from embed.FS at runtime. Invokes SpotiFLAC
   Python module with `--service <primary>,<fallback1>,<fallback2>,...` where the
   fallback list comes from `SPF_FALLBACK_SERVICES` config (via `Client.fallbackServices`).
   If Python binary not found or wrapper fails (no `complete` event), falls through to CLI.

2. **CLI + custom API URL** — SpotiFLAC CLI with `--tidal-api-url` (resolved from primary +
   Tidal API fallback chain, auto-detecting hifi-api format). Skips community tier.

3. **CLI + FSL/Byparr auto-solve** — If `SPOTIFLAC_FSL_URL` is set, headless browser
   solves Turnstile captcha automatically.

4. **CLI community tier** — Manual/relay verification.

**Phase budgets are separate.** Phase 1 gets `SPF_PYTHON_BUDGET` (default 20m,
clamped to `SPF_JOB_TIMEOUT`); phases 2-4 get their own full `SPF_JOB_TIMEOUT`
under the job context. They used to share one deadline, so a Python run stuck
on a dead service consumed the whole budget and the CLI started into an
already-dead context (`start spotiflac: context deadline exceeded`) - the
fallback never ran during outages. `Client.DownloadCLI` exposes phases 2-4
alone; `Client.HasPythonBackend()` reports whether phase 1 can run at all.

**Session-aware phase-1 skip (default on).** When the CLI's community session
store (`$HOME/.spotiflac/community_session.json`, override with
`SPF_COMMUNITY_SESSION_FILE`) holds a session whose `expires_at` is more than
120 s in the future AND the job's service is CLI-implemented (tidal/qobuz/
amazon), `Download()` skips phase 1 entirely and goes straight to the CLI
backends. The Python extensions authenticate with their own zarz mobile
sessions against the same spotbye infrastructure the desktop session signs
for, so with a valid session they can only repeat the outcome after burning
~10 min of provider budgets per job (measured 2026-08-22). Deezer primaries
keep phase 1: it is the only backend that implements deezer. Disable with
`SPF_SKIP_PYTHON_WHEN_SESSION_PRESENT=false`.

### Go-level retry & fallback (processDownload)

After the Python→CLI cascade, the Go handler adds its own retry/fallback loop:
- Primary service: 3 attempts with 5s/15s backoff, clearing job dir between retries
- Fallback chain (`SPF_FALLBACK_SERVICES`): each service gets 1 attempt, and it is
  **CLI-only when the Python backend is available** (`h.client.DownloadCLI`) -
  the wrapper's internal cascade already tried every service for the release,
  so re-running it per fallback only repeated failures while burning budget.
  Without Python, the full cascade runs per service as before.
- The job context carries a wall-clock deadline of `2×SPF_JOB_TIMEOUT` measured
  from PROCESSING START (not `job.TimeAdded` - queue wait must not consume the
  budget; jobs queued through an outage used to arrive already expired, 2026-08-22);
  every phase derives its deadline from it, so an expired budget kills in-flight
  subprocesses instead of letting them run on.
- Per-service circuit breaker: opens after 5 consecutive failures for 10 minutes
- Circuit breaker failures are attributed to the primary service, not the fallback
- Breaker input is real service failures only: a dead job context (budget expiry,
  cancellation) marks the job failed but does NOT feed `RecordFailure` - budget
  deaths once opened the breaker right after an outage lifted and fast-failed
  healthy-API jobs with "circuit open" (2026-08-22)
- Upstream community break gate (`upstream_break.go`): when a final job error
  carries the spotbye infra's scheduled-cooldown message ("short break ... try
  again in about N minute(s)"), the queue parks until now+N BEFORE jobs take a
  concurrency slot - jobs stay Queued, zero requests hit the dead infra during
  the window, and the backlog drains serially when the break lifts. In-memory
  only: a container restart during a break loses the window (one round of
  hammering re-arms it). Visible via SABnzbd `mode=warnings` (`upstream_break`).

### SpotiFLAC failover patches (patches/python/)

All patch scripts run at image build against the pinned `SpotiFLAC==3.0.6`
and fail the build loudly if a pattern is missing:

- `per_loop_lock.py` — per-loop asyncio locks (Deezer "bound to a different
  event loop") plus the extension-bridge timeout raises (120s→900s call/
  provider timeouts, 60s→600s bridge wait, 10s→60s Turnstile poll).
- `provider_breaker.py` — fast cross-service failover in `downloader.py`:
  per-provider budget cap (`SPOTIFLAC_PROVIDER_BUDGET_S`, default 300s) so a
  dead service cannot starve the ones behind it within a track, and a
  cross-track breaker (`SPOTIFLAC_PROVIDER_BREAKER_FAILURES`, default 1) that
  skips a failed provider for the rest of the album. A provider hitting its
  cap hands the remaining track budget to the next provider (the stock code
  treats any TimeoutError as track-over; the patch splits that).
- `download_api_ua.py` — the signed-session client no longer identifies as
  `SpotiFLAC-Mobile/<ver>`: Cloudflare-fronted download APIs (measured:
  zarz.moe) 403-block that User-Agent on /bootstrap while serving a solvable
  Turnstile challenge to a browser UA from the identical IP. The header is
  now `SPOTIFLAC_DOWNLOAD_API_UA`-overridable, defaulting to desktop Chrome.

Do not raise these ceilings without raising the budgets above them too - the
ceilings exist so legitimate slow downloads finish, the budgets exist so dead
services fail over.

### Key types

- `spotiflac.Client` — holds CLI path, Python venv path, Tidal API URLs,
  `fallbackServices []string`, FSL/relay config. Created in `cmd/server/main.go`.
- `spotiflac.ProgressEvent` — JSON-line event from subprocess stdout.
  `parseProgress` reads lines, dispatches by `type` field.
- `CollectPythonResult` (exported) — drains Python channels, gates on
  `complete` event. Returns false if Python didn't succeed → CLI fallback.

## Deployment constraint: ALL download traffic MUST stay behind the VPN

**Every download attempt (Python backend, CLI backends, every provider,
every Tidal/Qobuz mirror) must egress through the VPN tunnel.** On
potatostack that is why the container runs `network_mode: service:gluetun`
- do NOT "simplify" it onto the normal bridge network, and do not route
individual providers or API calls around the tunnel.

Consequences for work on this repo:

- Mirror/API health probes are only valid from INSIDE the container (VPN
  egress). A 200 from the host's ISP line proves nothing - Cloudflare-fronted
  services answer differently per source IP (measured 2026-08-21: samidy 200
  from ISP, degraded/403 from the tunnel).
- The verify-relay address fix (`resolveRelayURL`) does NOT violate this: it
  only changes how an EXTERNAL solver (trawl/FSL, outside the tunnel) reaches
  our inbound `/api/verify-relay` callback over the compose bridge IP. All
  outbound download traffic still leaves through the tunnel. A loopback FSL
  address (solver sharing our netns, e.g. trawl behind gluetun at
  `http://127.0.0.1:8191`) builds NO relay at all - the solver's browser
  reaches the CLI's own loopback callback directly, which is also the only
  cb shape spotbye's verify server accepts.
- If a change would make any request leave the container without going
  through the tunnel, stop and reconsider.

## Conventions

- **Commits:** Conventional commits — `feat:`, `fix:`, `chore:`, `docs:`, `test:`, `ci:`, `refactor:`
- **Tests:** Table-driven, `testify/assert` + `testify/require`. Test files in same package with `_test` suffix.
- **HTTP:** fiber/v3. Handlers use dependency injection via struct methods. No global state.
- **Logging:** zerolog. Structured JSON. Pass logger via `SetLogger()` on handlers.
- **Error handling:** Wrap errors with `fmt.Errorf("context: %w", err)`. Never panic in handlers.
- **Config:** All via env vars prefixed `SPF_`. See `internal/config/config.go` for defaults.

## Upstream Dependencies

- **Lidarr SABnzbd client:** `Lidarr/Lidarr: src/NzbDrone.Core/Download/Clients/Sabnzbd/Sabnzbd.cs`
- **SpotiFLAC:** `fishingpvalues/SpotiFLAC` (fork of `spotbye/SpotiFLAC`)
- **SABnzbd API spec:** `sabnzbd.org/wiki/configuration/5.0/api`

## Building & Testing

```bash
go build ./cmd/server           # Build
go test ./... -count=1          # Unit tests
INTEGRATION=1 go test ./tests/integration/... -v  # Integration (needs docker-compose up)
docker compose up -d            # Run with Lidarr
```

## Testing Patterns

### Mock subprocess tests

Both the SpotiFLAC CLI and Python wrapper are tested via mock shell scripts that
emit JSON lines and create dummy output files. Pattern:

```go
// Mock CLI/Python as a bash script in t.TempDir()
script := filepath.Join(t.TempDir(), "spotiflac-cli")
require.NoError(t, os.WriteFile(script, []byte(`#!/bin/bash
echo '{"type":"complete","path":"/tmp/out.flac","size":1000}'
`), 0755))

client := spotiflac.NewClient(script, timeout, ...)
events, errs := client.Download(ctx, url, outputDir, service, quality)
```

### Cascade tests (client_cascade_test.go)

Test the Python→CLI fallback order without real downloads:

- `TestCollectPythonResultForwardsAfterComplete` — only forwards events after `complete`
- `TestCollectPythonResultReturnsFalseOnNoComplete` — signals CLI fallback
- `TestDownloadPythonSucceedsCLINotInvoked` — Python emits complete, CLI never called
- `TestDownloadPythonFailsFallsThroughToCLI` — Python errors, CLI succeeds
- `TestDownloadPythonNotAvailableSkipsToCLI` — no Python binary, goes straight to CLI
- `TestDownloadServiceCascadeUsesConfiguredFallbacks` — `--service` gets primary+fallbacks
- `TestDownloadServiceCascadeExcludesDuplicatePrimary` — dedup in service list

### Handler-level retry/fallback tests (handler_test.go)

Test Go-level retry, fallback chain, and circuit breaker with mock CLI scripts
that read `--service` to simulate per-service behavior.

## Docker

Multi-stage build. Stage 1: proxy binary. Stage 2: SpotiFLAC CLI from fork (Go 1.26). Stage 3: alpine:3.21 runtime. Shared volume `/downloads` for Lidarr import.

The runtime user is created with an **explicit** uid/gid (`PUID`/`PGID` build
args, default 1000), not `adduser -S`. A system user picked id 100:101 while
deployments run the container as `1000:1000`, so `$HOME` was not writable by
the running process - and that alone broke the entire Python backend, because
Chromium cannot create its crashpad database under an unwritable home and
aborts with SIGTRAP. `chromium`, `nodejs` and `xvfb` are installed in the
image for the same backend; they used to be absent entirely.

## Lidarr's addfile request carries almost nothing

Lidarr's real grab is `mode=addfile`, and its POST query is only
`mode=addfile&cat=&priority=&apikey=&output=json` - no `nzbname`, no size, no
track count. Anything the download client needs about a release therefore has
to travel inside the synthetic NZB: `internal/indexer.Release` is written by
`t=get` (which reads `name`, `size` and `tracks` off the download URL that
`NewznabXML` built) and read back by `handleAddURL`. Leaving `Job.Filename`
empty marshals the queue slot as `"filename": ""`, and Lidarr keys its tracked
download off that string - the download completes and Lidarr never sees it.

## API Compatibility

Lidarr expects specific field types from SABnzbd:
- Queue: `mb`/`mbleft`/`mbmissing` as float64 (MB), `diskspace*` as float64 (GB), `timeleft` as "HH:MM:SS"
- History: `size` as int64 (bytes), `storage` as filesystem path
- `fullstatus` endpoint with `complete_dir` for v2.0+
- `get_config` must have `Misc.complete_dir`, `Misc.pre_check`, `Misc.history_retention`

See CI job `upstream-check.yml` for automated compatibility verification.
