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

### Go-level retry & fallback (processDownload)

After the Python→CLI cascade, the Go handler adds its own retry/fallback loop:
- Primary service: 3 attempts with 5s/15s backoff, clearing job dir between retries
- Fallback chain (`SPF_FALLBACK_SERVICES`): each service gets 1 attempt
- Per-service circuit breaker: opens after 5 consecutive failures for 10 minutes
- Circuit breaker failures are attributed to the primary service, not the fallback

### Key types

- `spotiflac.Client` — holds CLI path, Python venv path, Tidal API URLs,
  `fallbackServices []string`, FSL/relay config. Created in `cmd/server/main.go`.
- `spotiflac.ProgressEvent` — JSON-line event from subprocess stdout.
  `parseProgress` reads lines, dispatches by `type` field.
- `CollectPythonResult` (exported) — drains Python channels, gates on
  `complete` event. Returns false if Python didn't succeed → CLI fallback.

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

## API Compatibility

Lidarr expects specific field types from SABnzbd:
- Queue: `mb`/`mbleft`/`mbmissing` as float64 (MB), `diskspace*` as float64 (GB), `timeleft` as "HH:MM:SS"
- History: `size` as int64 (bytes), `storage` as filesystem path
- `fullstatus` endpoint with `complete_dir` for v2.0+
- `get_config` must have `Misc.complete_dir`, `Misc.pre_check`, `Misc.history_retention`

See CI job `upstream-check.yml` for automated compatibility verification.
