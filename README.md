# Spotiflac-Lidarr Proxy

> [!NOTE]
> This project was planned and implemented with AI assistance (Anthropic Claude Code). All AI-generated code is reviewed and tested before merging. See [AI Usage](#ai-usage) for details.

Bridge Lidarr ↔ SpotiFLAC. Implements SABnzbd download client API and Newznab indexer API so Lidarr treats this proxy as a standard Usenet downloader. The proxy shells out to a headless SpotiFLAC CLI to download high-quality FLAC files from Tidal, Qobuz, Amazon Music, and Deezer.

## How It Works

```
Lidarr → (Newznab search) → Proxy → (Spotify metadata) → SpotiFLAC
Lidarr → (SABnzbd addurl) → Proxy → (queue + exec) → SpotiFLAC CLI → FLAC files
Lidarr → (SABnzbd queue)  → Proxy → job status
Lidarr → Import FLAC from shared /downloads volume
```

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
   - URL Base: `/api/sabnzbd`
   - API Key: your SPF_API_KEY value
   - Category: `music-flac-16`

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
go build ./cmd/server
./server serve
```

## Testing

```bash
# Unit tests
go test ./... -count=1

# Integration tests (requires docker-compose up)
INTEGRATION=1 go test ./tests/integration/... -v
```

## Project Structure

```
cmd/server/          Entry point
internal/api/        SABnzbd + Newznab handlers
internal/spotiflac/  SpotiFLAC CLI wrapper
internal/queue/      SQLite job queue
internal/indexer/    Spotify metadata → Newznab XML
internal/storage/    File system operations
```

## AI Usage

This project was planned, architected, and implemented with assistance from Anthropic Claude (Claude Code). AI contributions include: architecture design, code generation, test authoring, CI/CD pipeline setup, and documentation. All code is human-reviewed and tested.

## License

MIT
