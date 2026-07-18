# Spotiflac-Lidarr Proxy

> [!NOTE]
> This project was planned and implemented with AI assistance (Anthropic Claude Code). All AI-generated code is reviewed and tested before merging. See [AI Usage](#ai-usage) for details.

Bridge Lidarr ↔ SpotiFLAC. Implements SABnzbd download client API and Newznab indexer API so Lidarr treats this proxy as a standard Usenet downloader. The proxy shells out to a headless SpotiFLAC CLI to download high-quality FLAC files from Tidal, Qobuz, Amazon Music, and Deezer.

## Architecture

```
                                   Lidarr
  ┌─────────────────────────────────────────────────────────────────────┐
  │  ┌──────────────────────┐         ┌──────────────────────────────┐  │
  │  │  Download Client      │         │  Indexer                     │  │
  │  │  (SABnzbd mode)       │         │  (Newznab mode)              │  │
  │  └──────────┬───────────┘         └──────────────┬───────────────┘  │
  └─────────────┼────────────────────────────────────┼──────────────────┘
                │                                     │
        ┌───────▼─────────────────────────────────────▼────────────────┐
        │               spotiflac-lidarr-proxy                          │
        │                                                               │
        │  ┌─────────────────────────┐    ┌──────────────────────────┐  │
        │  │  /api (SABnzbd)         │    │  /api/newznab             │  │
        │  │  /api/sabnzbd           │    │                          │  │
        │  │                         │    │  t=caps → capabilities   │  │
        │  │  mode=version           │    │  t=search → Spotify      │  │
        │  │  mode=auth              │    │  t=music → album search  │  │
        │  │  mode=get_config        │    │  t=details → item info   │  │
        │  │  mode=get_cats          │    │                          │  │
        │  │  mode=fullstatus        │    └──────┬───────────────────┘  │
        │  │  mode=addurl/addfile    │           │                      │
        │  │  mode=queue             │    ┌──────▼───────────────────┐  │
        │  │  mode=history           │    │  internal/indexer/        │  │
        │  │  mode=retry             │    │  Spotify search → XML    │  │
        │  │  mode=delete            │    └──────────────────────────┘  │
        │  │  mode=pause/resume      │                                  │
        │  │  mode=change_cat        │    ┌──────────────────────────┐  │
        │  │  mode=server_stats      │    │  Job Queue (SQLite)       │  │
        │  │  mode=status            │    │  ┌─────┐ ┌─────┐ ┌────┐ │  │
        │  │  mode=warnings          │    │  │ J1 │ │ J2 │ │ J3 │ │  │
        │  │  mode=pause_all         │    │  └──┬──┘ └──┬──┘ └──┬──┘ │  │
        │  │  mode=resume_all        │    │     │       │       │    │  │
        │  │  mode=set_speedlimit    │    │     ▼       ▼       ▼    │  │
        │  └─────────────────────────┘    │  ┌────────────────────┐  │  │
        │                                  │  │  SpotiFLAC CLI    │  │  │
        │                                  │  │  (subprocess)     │  │  │
        │                                  │  │                    │  │  │
        │                                  │  │  --url <spotify>   │  │  │
        │                                  │  │  --service tidal   │  │  │
        │                                  │  │  --quality lossless│  │  │
        │                                  │  │  --output-dir <dir>│  │  │
        │                                  │  └────────┬───────────┘  │  │
        │                                  │           │              │  │
        │                                  │    ┌──────▼──────────┐   │  │
        │                                  │    │ Tidal / Qobuz    │   │  │
        │                                  │    │ Amazon / Deezer  │   │  │
        │                                  │    └──────┬──────────┘   │  │
        │                                  │           │              │  │
        │                                  │    ┌──────▼──────────┐   │  │
        │                                  │    │  FLAC files      │   │  │
        │                                  │    │  → /downloads/   │   │  │
        │                                  │    └─────────────────┘   │  │
        │                                  └──────────────────────────┘  │
        └────────────────────────────────────────────────────────────────┘
                          │
                   ┌──────▼──────────────────────────────────────────────┐
                   │  Lidarr Import                                      │
                   │  Lidarr scans /downloads/, imports FLAC, renames,    │
                   │  tags, and organizes into your music library         │
                   └─────────────────────────────────────────────────────┘
```

## API Routes

### SABnzbd Download Client API (`/api` or `/api/sabnzbd`)

| Lidarr Action           | HTTP Request                              | Handler            | Status |
|--------------------------|-------------------------------------------|--------------------|--------|
| Test connection          | `GET /api?mode=version`                   | `handleVersion`    | Done   |
| Authorization check      | `GET /api?mode=auth`                      | `handleAuth`       | Done   |
| Get config               | `GET /api?mode=get_config`                | `handleGetConfig`  | Done   |
| Get categories           | `GET /api?mode=get_cats`                 | `handleGetCats`    | Done   |
| Full status              | `GET /api?mode=fullstatus`               | `handleFullStatus` | Done   |
| Add download             | `GET /api?mode=addurl&name=<spotify>`    | `handleAddURL`     | Done   |
| Queue status             | `GET /api?mode=queue`                    | `handleQueue`      | Done   |
| History                  | `GET /api?mode=history`                  | `handleHistory`    | Done   |
| Pause job                | `GET /api?mode=queue&name=pause`         | `handlePause`      | Done   |
| Resume job               | `GET /api?mode=queue&name=resume`        | `handleResume`     | Done   |
| Delete job               | `GET /api?mode=queue&name=delete`        | `handleDelete`     | Done   |
| Retry failed             | `GET /api?mode=retry`                    | `handleRetry`      | Done   |
| Server stats             | `GET /api?mode=server_stats`             | `handleServerStats`| Done   |
| Warnings                 | `GET /api?mode=warnings`                 | `handleWarnings`   | Done   |
| Change category          | `GET /api?mode=change_cat`               | `handleChangeCat`  | Done   |
| Pause all                | `GET /api?mode=pause_all`                | `handlePauseAll`   | Done   |
| Resume all               | `GET /api?mode=resume_all`               | `handleResumeAll`  | Done   |
| Set speed limit          | `GET /api?mode=set_speedlimit`           | `handleSetSpeedlimit`| Done |

### Newznab Indexer API (`/api/newznab`)

| Lidarr Action           | HTTP Request                              | Handler            | Status |
|--------------------------|-------------------------------------------|--------------------|--------|
| Capabilities             | `GET /api/newznab?t=caps`                | `handleCaps`       | Done   |
| Search                   | `GET /api/newznab?t=search&q=<query>`    | `handleSearch`     | Done   |
| Music search             | `GET /api/newznab?t=music&artist=<a>`    | `handleMusic`      | Done   |
| Item details             | `GET /api/newznab?t=details&id=<id>`     | `handleDetails`    | Done   |

### Queue Response Fields

| Field           | Type      | Description                        |
|-----------------|-----------|------------------------------------|
| status          | string    | `Idle`, `Downloading`, `Paused`   |
| speed           | string    | Human-readable download speed      |
| kbpersec        | string    | KiloBytes/sec                      |
| timeleft        | string    | `HH:MM:SS` ETA for all jobs        |
| mb              | float64   | Total MB of all queued jobs        |
| mbleft          | float64   | Total MB remaining                 |
| slots           | array     | Individual job slots               |
| diskspace1/2    | float64   | Free disk space in GB              |

### Slot Fields

| Field        | Type    | Description                       |
|--------------|---------|-----------------------------------|
| status       | string  | `Queued`, `Downloading`, `Paused` |
| nzo_id       | string  | Unique job identifier             |
| filename     | string  | Artist - Album name               |
| mb           | float64 | Job size in MB                    |
| mbleft       | float64 | MB remaining                      |
| mbmissing    | float64 | MB missing (always 0 for Spotify) |
| percentage   | string  | Progress percentage               |
| timeleft     | string  | `HH:MM:SS` per-job ETA            |
| time_added   | int64   | Unix timestamp when added         |
| cat          | string  | Category                          |

### History Response Fields

| Field         | Type   | Description                        |
|---------------|--------|------------------------------------|
| status        | string | `Completed` or `Failed`           |
| name          | string | Artist - Album name               |
| size          | int64  | Size in bytes                     |
| completed     | int64  | Unix timestamp of completion      |
| download_time | int    | Duration in seconds               |
| storage       | string | Output directory path             |
| fail_message  | string | Error message on failure          |

## Quick Start

### Docker Compose

```yaml
services:
  proxy:
    image: ghcr.io/fishingpvalues/spotiflac-lidarr-proxy:latest
    ports: ["8484:8484"]
    environment:
      - SPF_API_KEY=your-secret-key
      - SPF_OUTPUT_DIR=/downloads
    volumes:
      - downloads:/downloads
  lidarr:
    image: lscr.io/linuxserver/lidarr:latest
    ports: ["8686:8686"]
    environment:
      - PUID=1000
      - PGID=1000
    volumes:
      - downloads:/downloads
      - config:/config
```

### Lidarr Setup

1. **Download Client:** Settings → Download Clients → Add → SABnzbd
   - Host: `proxy`, Port: `8484`
   - URL Base: leave empty (proxy handles `/api` route)
   - API Key: your SPF_API_KEY value
   - Category: `music`

2. **Indexer:** Settings → Indexers → Add → Newznab
   - URL: `http://proxy:8484/api/newznab`
   - API Key: your SPF_API_KEY value
   - Categories: 3010, 3040

## Configuration

All via environment variables prefixed `SPF_`:

| Variable | Default | Description |
|----------|---------|-------------|
| SPF_PORT | 8484 | HTTP listen port |
| SPF_API_KEY | (required) | API key for Lidarr auth |
| SPF_OUTPUT_DIR | /downloads | FLAC output directory |
| SPF_SPOTIFLAC_CLI_PATH | /usr/local/bin/spotiflac-cli | SpotiFLAC binary |
| SPF_DEFAULT_SERVICE | tidal | Download service priority |
| SPF_DEFAULT_QUALITY | lossless | Quality: lossless, hires, both |
| SPF_MAX_CONCURRENT | 3 | Max concurrent downloads |
| SPF_JOB_TIMEOUT | 30m | Max time per download |
| SPF_DB_PATH | /data/queue.db | SQLite database path |
| SPF_LOG_LEVEL | info | Log level |

## Building

```bash
# Native build
go build ./cmd/server
./server serve

# Docker multi-arch build
docker buildx create --use --name multiarch 2>/dev/null || true
docker buildx build --platform linux/amd64,linux/arm64 -t proxy:test .
```

## Testing

```bash
# Unit tests
go test ./... -count=1

# Integration tests (requires docker-compose up)
INTEGRATION=1 go test ./tests/integration/... -v
```

## CI/CD

Three workflows:

| Workflow | Trigger | Artifacts |
|----------|---------|-----------|
| **CI** | Push/PR to main | Go lint, test, build, multi-arch Docker dry-run |
| **Release** | Push tag `v*` | Multi-arch Docker images to GHCR (`latest`, semver) |
| **Beta** | Push to main | Multi-arch Docker images to GHCR (`beta`, `sha-<hash>`) |

## Project Structure

```
cmd/server/           Entry point
internal/api/         SABnzbd + Newznab HTTP handlers
internal/spotiflac/   SpotiFLAC CLI wrapper
internal/queue/       SQLite job queue
internal/indexer/     Spotify metadata → Newznab XML
internal/storage/     File system operations
pkg/sabnzbd/          Shared SABnzbd API types
```

## AI Usage

This project was planned, architected, and implemented with assistance from Anthropic Claude (Claude Code). AI contributions include: architecture design, code generation, test authoring, CI/CD pipeline setup, and documentation. All code is human-reviewed and tested.

## License

MIT
