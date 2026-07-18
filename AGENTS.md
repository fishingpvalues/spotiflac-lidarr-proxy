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
internal/spotiflac/  SpotiFLAC CLI subprocess wrapper
internal/queue/      SQLite-backed job queue (modernc.org/sqlite)
internal/indexer/    Spotify metadata → Newznab XML
internal/storage/    File system operations
internal/config/     Viper config (env vars prefixed SPF_)
pkg/sabnzbd/         Shared SABnzbd JSON types
tests/integration/   Integration tests (docker-compose)
```

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

## Docker

Multi-stage build. Stage 1: proxy binary. Stage 2: SpotiFLAC CLI from fork (Go 1.26). Stage 3: alpine:3.21 runtime. Shared volume `/downloads` for Lidarr import.

## API Compatibility

Lidarr expects specific field types from SABnzbd:
- Queue: `mb`/`mbleft`/`mbmissing` as float64 (MB), `diskspace*` as float64 (GB), `timeleft` as "HH:MM:SS"
- History: `size` as int64 (bytes), `storage` as filesystem path
- `fullstatus` endpoint with `complete_dir` for v2.0+
- `get_config` must have `Misc.complete_dir`, `Misc.pre_check`, `Misc.history_retention`

See CI job `upstream-check.yml` for automated compatibility verification.
