# Spotiflac-Lidarr Proxy — Design Spec

**Date:** 2026-07-18
**Status:** Approved
**Repo:** `github.com/fishingpvalues/spotiflac-lidarr-proxy`

## Overview

Go service that bridges Lidarr ↔ SpotiFLAC. Implements SABnzbd download client API + Newznab indexer API so Lidarr treats the proxy as a standard Usenet downloader. Proxy shells out to a headless SpotiFLAC CLI (forked from `spotbye/SpotiFLAC`), downloads FLAC files, places them in a shared volume for Lidarr import.

Full Tidarr-style integration: Lidarr searches Spotify metadata in-app (Newznab), sends downloads to the proxy (SABnzbd), proxy handles SpotiFLAC execution, Lidarr imports completed FLAC.

## Architecture

**Approach:** Pragmatic monolith with well-layered internal packages. Single binary, SQLite-backed queue, SpotiFLAC CLI as subprocess.

```
                    ┌──────────────────────────────┐
                    │         Lidarr                │
                    │  (existing docker container)  │
                    └──────┬──────────────┬────────┘
                           │ Newznab      │ SABnzbd
                           ▼              ▼
                    ┌──────────────────────────────┐
                    │    spotiflac-lidarr-proxy     │
                    │  ┌─────────┐  ┌─────────┐    │
                    │  │ Newznab │  │ SABnzbd │    │
                    │  │ handler │  │ handler  │    │
                    │  └────┬────┘  └────┬─────┘    │
                    │       │            │          │
                    │  ┌────▼────┐  ┌────▼─────┐    │
                    │  │ Indexer │  │  Queue    │    │
                    │  │ (search)│  │ (SQLite)  │    │
                    │  └────┬────┘  └────┬─────┘    │
                    │       │            │          │
                    │  ┌────▼────────────▼──────┐    │
                    │  │   SpotiFLAC client      │    │
                    │  │   (exec subprocess)     │    │
                    │  └──────────┬─────────────┘    │
                    └─────────────┼──────────────────┘
                                  │
                    ┌─────────────▼──────────────────┐
                    │  SpotiFLAC CLI (headless fork) │
                    │  Tidal/Qobuz/Amazon/Deezer     │
                    └─────────────┬──────────────────┘
                                  │
                    ┌─────────────▼──────────────────┐
                    │  /downloads (shared volume)    │
                    │  FLAC files + metadata         │
                    └────────────────────────────────┘
```

## Project Structure

```
spotiflac-lidarr-proxy/
├── cmd/
│   └── server/main.go           # Entry point, wire dependencies
├── internal/
│   ├── api/
│   │   ├── sabnzbd/             # SABnzbd-compatible handlers
│   │   │   ├── handler.go       # Mode dispatch router
│   │   │   ├── addurl.go        # addurl/addfile
│   │   │   ├── queue.go         # queue, pause, resume, delete
│   │   │   ├── history.go       # history endpoint
│   │   │   └── status.go        # version, auth, get_config, get_cats
│   │   ├── newznab/             # Newznab-compatible indexer
│   │   │   ├── handler.go       # t=caps/search/music/details dispatch
│   │   │   └── search.go        # Spotify metadata → Newznab XML
│   │   └── middleware.go        # API key auth, logging, recovery
│   ├── spotiflac/
│   │   ├── client.go            # Subprocess exec + context management
│   │   └── progress.go          # Parse JSON progress lines from stdout
│   ├── queue/
│   │   ├── queue.go             # SQLite-backed job queue
│   │   └── job.go               # Job model + state machine
│   ├── indexer/
│   │   ├── spotify.go           # Spotify metadata via SpotiFLAC CLI
│   │   └── newznab.go           # Map results to Newznab XML format
│   ├── config/
│   │   └── config.go            # Viper: env vars + optional config file
│   └── storage/
│       └── storage.go           # File system ops, output dir management
├── pkg/
│   └── sabnzbd/
│       └── types.go             # Shared SABnzbd JSON response types
├── tests/
│   ├── integration/             # Integration tests with real Lidarr
│   │   └── main_test.go
│   └── fixtures/                # Test fixtures
│       └── spotify_urls.json
├── Dockerfile                   # Multi-stage: Go build + spotiflac-cli
├── docker-compose.yml           # Lidarr + proxy for dev/testing
├── .github/workflows/
│   ├── ci.yml                   # Lint, test, build on push/PR
│   └── release.yml              # Trivy scan, Docker push, GH release on tag
├── renovate.json
├── lefthook.yml
├── .golangci.yml
├── go.mod
└── README.md
```

## API Surface

### SABnzbd Endpoints (`/api/sabnzbd`)

All requests require `apikey` parameter (except `version`, `auth`). Default output is JSON.

| Mode | Method | Parameters | Response |
|------|--------|------------|----------|
| `version` | GET | — | `{"version": "x.y.z"}` |
| `auth` | GET | — | `{"auth": true}` if API key valid |
| `get_config` | GET | — | Config with categories, scripts, speedlimits |
| `addurl` | POST | `name` (Spotify URL), `cat`, `priority`, `nzbname` | `{"status": true, "nzo_ids": ["..."]}` |
| `addfile` | POST | File upload with Spotify URLs | Same as addurl |
| `queue` | GET | `start`, `limit`, `search`, `nzo_ids` | `{"queue": {"slots": [...], ...}}` |
| `history` | GET | `start`, `limit`, `search` | `{"history": {"slots": [...], ...}}` |
| `queue&name=pause` | POST | `value` (nzo_id) | `{"status": true, "nzo_ids": [...]}` |
| `queue&name=resume` | POST | `value` (nzo_id) | Same as pause |
| `queue&name=delete` | POST | `value` (nzo_id), `del_files` | Same as pause |
| `get_cats` | GET | — | `{"categories": ["music-flac-16", "music-flac-24", "music-mp3"]}` |
| `change_cat` | POST | `value` (nzo_id), `value2` (category) | `{"status": true}` |

**Queue slot mapping to SpotiFLAC jobs:**

| SABnzbd field | SpotiFLAC mapping |
|---------------|-------------------|
| `nzo_id` | UUID generated on job creation |
| `status` | `Downloading` while SpotiFLAC runs, removed on complete |
| `filename` | Artist - Album (parsed from Spotify URL metadata) |
| `size` / `sizeleft` | Estimated from quality setting, decrement via progress |
| `percentage` | Parsed from SpotiFLAC JSON progress lines |
| `cat` | `music-flac-16`, `music-flac-24`, or `music-mp3` |
| `timeleft` | Estimated from current download rate |
| `priority` | `Normal` (0) default, configurable |

**History slot mapping:**

| Status | Condition |
|--------|-----------|
| `Completed` | SpotiFLAC exit code 0, FLAC files present |
| `Failed` | SpotiFLAC exit code non-zero, or timeout |
| `Failed/Retry` | Transient error, Lidarr will retry |

### Newznab Endpoints (`/api/newznab`)

All requests require `apikey` parameter. Response is XML (RSS/Newznab format).

| Parameter | Purpose |
|-----------|---------|
| `t=caps` | Capabilities: search available, music categories (3000/3010/3040) |
| `t=search&q=...` | Free-text search. Proxy calls SpotiFLAC metadata search. |
| `t=music&artist=...&album=...` | Structured music search. Exact match preferred. |
| `t=details&id=...` | Album details, tracklist, cover URL. |

Search uses SpotiFLAC's Spotify metadata lookup. SpotiFLAC can resolve Spotify URLs/track IDs to full metadata (ISRC, cover, genres, tracklist). The indexer caches results briefly (60s in-memory) to avoid repeated CLI calls.

Newznab XML response maps:
- `<item>` per album/track result
- `<enclosure url="..." length="..." type="application/x-nzb"/>` with Spotify URL as payload
- `<attr name="artist">`, `<attr name="album">`, `<attr name="genre">`
- Lidarr grabs the Spotify URL, sends it as NZB to SABnzbd `addurl`

## Data Flow

### Search → Download → Import

```
Lidarr                    Proxy                     SpotiFLAC CLI
  │                         │                           │
  │─ t=music&artist=X       │                           │
  │────────────────────────>│                           │
  │                         │─ metadata search ────────>│
  │                         │<─ album list JSON ────────│
  │<─ Newznab RSS/XML ──────│                           │
  │                         │                           │
  │─ mode=addurl&name=URL   │                           │
  │────────────────────────>│                           │
  │                         │─ INSERT into SQLite queue  │
  │<─ {nzo_ids: [id]} ──────│                           │
  │                         │─ goroutine: exec CLI ────>│
  │                         │                           │── download FLAC
  │                         │                           │── tag + cover art
  │─ mode=queue             │                           │
  │────────────────────────>│                           │
  │<─ {slots: [{status}]} ──│                           │
  │                         │<─ JSON progress lines ────│
  │                         │                           │
  │                         │<─ exit code 0 ────────────│
  │                         │─ UPDATE job → completed    │
  │                         │─ move files to /downloads  │
  │                         │                           │
  │─ mode=history           │                           │
  │────────────────────────>│                           │
  │<─ {slots: [{Completed}]}│                           │
  │                         │                           │
  │─ Import /downloads/     │                           │
  │─────────────────────────────────────────────────────│
```

### Job State Machine

```
  addurl ──> Queued ──> Downloading ──> Completed ──> (history)
                  │            │
                  │            └──> Failed ──> (history, Lidarr retries)
                  │
                  ├──> Paused ──> Queued (resume)
                  │
                  └──> Deleted
```

## SpotiFLAC Fork

**Repo:** `github.com/fishingpvalues/SpotiFLAC` (fork of `spotbye/SpotiFLAC`)

**Changes from upstream:**
1. Add `cmd/spotiflac-cli/main.go` — headless CLI entry point
2. CLI flags:
   - `--url <spotify-url>` — Spotify track/album/playlist URL
   - `--output-dir <path>` — where to write FLAC files
   - `--service <tidal|qobuz|amazon|deezer>` — download service priority
   - `--quality <lossless|hires|both>` — quality preference
   - `--format "{artist}/{album}/{track} - {title}.flac"` — output path template
   - `--json-progress` — emit JSON progress lines on stdout
   - `--timeout <duration>` — max download time
3. Build tag `headless` to exclude Wails GUI dependencies
4. JSON progress format (one line per event):
   ```json
   {"type":"progress","track":"01","title":"Song Name","percent":45,"speed":"1.2MB/s"}
   {"type":"metadata","artist":"Artist","album":"Album","isrc":"..."}
   {"type":"complete","path":"/output/Artist/Album/01 - Song.flac","size":28765432}
   {"type":"error","message":"rate limited, retrying in 30s"}
   ```
5. Exit codes: 0 = success, 1 = error, 2 = rate-limited (retryable)

**Build integration:**
- Proxy Dockerfile clones the fork and builds `spotiflac-cli` in the Go build stage
- Binary placed at `/usr/local/bin/spotiflac-cli` in runtime image
- Path configurable via `SPOTIFLAC_CLI_PATH` env var

## Tech Stack

| Concern | Choice | Rationale |
|---------|--------|-----------|
| HTTP router | `github.com/gofiber/fiber/v3` | Fast, low-alloc, good query param handling for SABnzbd modes |
| Config | `github.com/spf13/viper` | Env vars + YAML config file, standard in Go ecosystem |
| Logging | `github.com/rs/zerolog` | Structured JSON, zero-alloc, context-aware |
| Queue store | `modernc.org/sqlite` | Pure Go SQLite, no CGO, survives restarts |
| CLI flags (server) | `github.com/spf13/cobra` | Standard, composable subcommands |
| HTTP client (Lidarr) | `net/http` + custom client | Minimal dependency |
| Testing | `github.com/stretchr/testify` + `net/http/httptest` | Standard assertions + handler testing |
| Linting | `golangci-lint` | Comprehensive, configurable |
| XML generation | `encoding/xml` (stdlib) | Newznab responses |

## Configuration

Environment variables (all uppercase, prefixed with `SPF_`):

| Variable | Default | Description |
|----------|---------|-------------|
| `SPF_PORT` | `8484` | HTTP listen port |
| `SPF_API_KEY` | (required) | API key for Lidarr authentication |
| `SPF_OUTPUT_DIR` | `/downloads` | Where completed FLACs land |
| `SPF_SPOTIFLAC_CLI_PATH` | `/usr/local/bin/spotiflac-cli` | Path to SpotiFLAC CLI binary |
| `SPF_DEFAULT_SERVICE` | `tidal` | Default download service |
| `SPF_DEFAULT_QUALITY` | `lossless` | Default quality (lossless/hires/both) |
| `SPF_MAX_CONCURRENT` | `3` | Max concurrent downloads |
| `SPF_JOB_TIMEOUT` | `30m` | Max time per download job |
| `SPF_DB_PATH` | `/data/queue.db` | SQLite database path |
| `SPF_LOG_LEVEL` | `info` | Log level (debug/info/warn/error) |

## Docker

### Multi-stage Dockerfile

```
Stage 1 (go-builder):    golang:1.24-alpine
  Build proxy server binary
Stage 2 (cli-builder):   golang:1.24-alpine
  Clone SpotiFLAC fork, build spotiflac-cli with -tags headless
Stage 3 (runtime):       alpine:3.21
  Copy both binaries, ca-certificates, timezone data
  ENTRYPOINT ["/usr/local/bin/server"]
  EXPOSE 8484
```

### docker-compose.yml (dev + testing)

```yaml
services:
  proxy:
    build: .
    ports: ["8484:8484"]
    environment:
      - SPF_API_KEY=test-key-123
      - SPF_OUTPUT_DIR=/downloads
      - SPF_LOG_LEVEL=debug
    volumes:
      - downloads:/downloads
      - data:/data

  lidarr:
    image: lscr.io/linuxserver/lidarr:latest
    ports: ["8686:8686"]
    environment:
      - PUID=1000
      - PGID=1000
      - TZ=UTC
    volumes:
      - downloads:/downloads
      - lidarr_config:/config

volumes:
  downloads:
  data:
  lidarr_config:
```

## CI/CD

### GitHub Actions — ci.yml (on push, PR to main)

1. Checkout
2. Setup Go 1.24
3. `golangci-lint run` (from `.golangci.yml`)
4. `go test -race -cover ./...` (unit tests)
5. `go build ./cmd/server` (verify build)
6. `docker build -t proxy .` (smoke test Docker build)

### GitHub Actions — release.yml (on tag `v*`)

1. Checkout
2. Setup Go 1.24
3. Run Trivy vulnerability scan on deps
4. Build Docker image
5. Run Trivy scan on Docker image
6. Push to `ghcr.io/fishingpvalues/spotiflac-lidarr-proxy:latest` + version tag
7. Create GitHub Release via goreleaser or Release Please

### Lefthook (pre-commit)

- `gofmt -s -w .`
- `golangci-lint run --new-from-rev=HEAD~1`
- `commitlint` (conventional commits)

### Renovate

- Auto-PR for Go module updates
- Auto-PR for Docker base image updates
- Auto-PR for GitHub Actions updates

## Testing

### Unit Tests

- **SABnzbd handlers:** Table-driven tests with `httptest`. Mock SpotiFLAC client. Assert JSON responses match SABnzbd schema.
- **Newznab handlers:** Table-driven tests. Assert XML output matches Newznab schema.
- **Queue:** SQLite in-memory (`:memory:`). Test CRUD, state transitions, concurrent access.
- **Config:** Test env var parsing, defaults, validation.
- **Spotiflac client:** Mock exec. Test progress parsing, timeout handling, error mapping.
- **Indexer:** Mock SpotiFLAC metadata output. Test Newznab XML generation.

### Integration Tests

- Spin `docker-compose` with Lidarr + proxy
- Configure Lidarr via HTTP API: add proxy as SABnzbd download client + Newznab indexer
- Submit real Spotify URL (public playlist or album) via Lidarr API
- Poll proxy queue API until download completes
- Assert FLAC files exist in shared volume
- Assert Lidarr sees completed download via its API

### Smoke Tests

- `curl localhost:8484/api/sabnzbd?mode=version` → `{"version":"0.1.0"}`
- `curl localhost:8484/api/sabnzbd?mode=auth&apikey=test-key` → `{"auth":true}`
- `curl localhost:8484/api/sabnzbd?mode=get_cats&apikey=test-key` → categories JSON
- `curl localhost:8484/api/newznab?t=caps&apikey=test-key` → capabilities XML
- Health endpoint: `GET /health` → `{"status":"ok"}`

## Open Questions / Future

1. **Lidarr direct import:** Should proxy call Lidarr API to trigger import after download completes, or rely on Lidarr's periodic scan? Default: rely on Lidarr scan (simpler).
2. **Multi-artist albums:** SpotiFLAC handles this; verify edge cases in integration tests.
3. **Playlist expansion:** When Spotify playlist URL submitted, expand to individual albums or download as one batch? Default: expand to individual albums, one queue slot each.
4. **Rate limiting:** SpotiFLAC may rate-limit. Proxy should back off and retry. Map to SABnzbd `Retry` status so Lidarr retries.
5. **Webhook notifications:** Optional webhook on download complete (for automation beyond Lidarr).

## AI Usage Disclosure

This project's planning, architecture, and implementation were assisted by Anthropic Claude (Claude Code). All AI-generated code is reviewed and tested before merging. The README includes a prominent AI-usage badge and disclosure section per project policy.
