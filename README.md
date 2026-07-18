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


## Category System

The proxy exposes 17 categories that Lidarr can use to select service and quality. Categories follow the pattern `music-[service][-quality]`, parsed at download time to set the correct `--service` and `--quality` flags for SpotiFLAC CLI.

### SABnzbd Categories (Lidarr Download Client)

| Category | Quality | Service | SpotiFLAC --quality |
|----------|---------|---------|---------------------|
| music | Default | default (tidal) | LOSSLESS |
| music-flac-16 | CD Quality 16-bit | default (tidal) | LOSSLESS |
| music-flac-24 | Hi-Res 24-bit | default (tidal) | HIRES_LOSSLESS |
| music-lossless | Best available | default (tidal) | HIRES_LOSSLESS |
| music-mp3 | MP3 | default (tidal) | LOSSLESS |
| music-tidal | Best available | Tidal | HIRES_LOSSLESS |
| music-qobuz | Best available | Qobuz | HIRES_LOSSLESS |
| music-amazon | Best available | Amazon | HIRES_LOSSLESS |
| music-deezer | Best available | Deezer | HIRES_LOSSLESS |
| music-tidal-flac-16 | CD Quality | Tidal | LOSSLESS |
| music-tidal-flac-24 | Hi-Res | Tidal | HIRES_LOSSLESS |
| music-qobuz-flac-16 | CD Quality | Qobuz | LOSSLESS |
| music-qobuz-flac-24 | Hi-Res | Qobuz | HIRES_LOSSLESS |
| music-amazon-flac-16 | CD Quality | Amazon | LOSSLESS |
| music-amazon-flac-24 | Hi-Res | Amazon | HIRES_LOSSLESS |
| music-deezer-flac-16 | CD Quality | Deezer | LOSSLESS |
| music-deezer-flac-24 | Hi-Res | Deezer | HIRES_LOSSLESS |

### Newznab Categories (Lidarr Indexer)

| ID | Name | Maps To |
|----|------|---------|
| 3010 | Lossless | music-lossless |
| 3040 | FLAC 24-bit | music-flac-24 |
| 3050 | FLAC 16-bit | music-flac-16 |
| 3060 | Tidal | music-tidal |
| 3061 | Qobuz | music-qobuz |
| 3062 | Amazon | music-amazon |
| 3063 | Deezer | music-deezer |

### SpotiFLAC Service x Quality Matrix

| Service | LOSSLESS (16-bit FLAC) | HIRES_LOSSLESS (24-bit FLAC) |
|---------|----------------------|------------------------------|
| tidal | Yes (FLAC 44.1/16) | Yes (FLAC up to 192/24) |
| qobuz | Yes (FLAC 44.1/16) | Yes (FLAC up to 192/24) |
| amazon | Yes (FLAC 44.1/16) | Yes (FLAC up to 192/24) |
| deezer | Yes (FLAC 44.1/16) | Limited availability |

### Quality Mapping

When Lidarr adds a download with a specific category, the proxy extracts service and quality:

```
music-qobuz-flac-24  →  --service qobuz --quality HIRES_LOSSLESS
music-tidal-flac-16  →  --service tidal --quality LOSSLESS
music-flac-24        →  --service [default] --quality HIRES_LOSSLESS
music-amazon         →  --service amazon --quality HIRES_LOSSLESS (default)
```

This means users can pick:
- **Quality-based**: `music-flac-16` or `music-flac-24` — uses default service with desired quality
- **Service-based**: `music-tidal` — uses best quality on that service
- **Combined**: `music-qobuz-flac-24` — full control over service and quality
