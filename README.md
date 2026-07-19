<p align="center">
  <img src="docs/assets/icon.svg" width="96" height="96" alt="spotiflac-lidarr-proxy icon">
</p>

<h1 align="center">Spotiflac-Lidarr Proxy</h1>

<p align="center">
  <a href="https://github.com/fishingpvalues/spotiflac-lidarr-proxy/actions/workflows/ci.yml"><img src="https://github.com/fishingpvalues/spotiflac-lidarr-proxy/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/fishingpvalues/spotiflac-lidarr-proxy/releases/latest"><img src="https://img.shields.io/github/v/release/fishingpvalues/spotiflac-lidarr-proxy" alt="Latest release"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache%202.0-blue.svg" alt="License"></a>
  <a href="go.mod"><img src="https://img.shields.io/badge/go-1.25%2B-00ADD8.svg" alt="Go version"></a>
  <a href="https://github.com/fishingpvalues/spotiflac-lidarr-proxy/pkgs/container/spotiflac-lidarr-proxy"><img src="https://img.shields.io/badge/docker-ghcr.io-2496ED.svg" alt="Docker image"></a>
</p>

<p align="center">
  Lets Lidarr talk to SpotiFLAC.
</p>

> [!WARNING]
> Use this only to download content you have the legal right to download. See [Legal](#legal).

This proxy implements the SABnzbd download client API and the Newznab indexer API, so Lidarr treats it like an ordinary Usenet setup. Underneath, it drives a headless SpotiFLAC CLI to pull FLAC and hi-res audio from Tidal, Qobuz, Amazon Music, and Deezer, using Spotify links as the search key. No account or login required for any of the four services.

**Self-contained.** The published Docker image bundles a `spotiflac-cli` build from a pinned commit of a [maintained fork](https://github.com/fishingpvalues/SpotiFLAC), so there is no separate SpotiFLAC service to run or keep in sync.

## Getting started

```bash
git clone https://github.com/fishingpvalues/spotiflac-lidarr-proxy.git
cd spotiflac-lidarr-proxy
cp .env.example .env
# edit .env and set SPF_API_KEY to a random string
docker compose up -d
```

This builds the proxy and starts it next to a Lidarr container on the same Docker network, sharing a `/downloads` volume. To use a prebuilt image instead of building locally:

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

### Lidarr setup

1. **Download Client:** Settings -> Download Clients -> Add -> SABnzbd
   - Host: `proxy`, Port: `8484`
   - URL Base: leave empty
   - API Key: your `SPF_API_KEY` value
   - Category: `music`

2. **Indexer:** Settings -> Indexers -> Add -> Newznab
   - URL: `http://proxy:8484/api/newznab`
   - API Key: your `SPF_API_KEY` value
   - Categories: 3010, 3040

## Features

- Speaks both halves of Lidarr's protocol: SABnzbd (download client) and Newznab (indexer).
- Quality/service categories (`music-flac-24`, `music-tidal`, ...) map onto Lidarr's quality profiles.
- Confirms each download actually completed (event signal plus track-count check) before reporting success.
- Per-service circuit breaker, with an optional fallback chain across Tidal/Qobuz/Amazon/Deezer.
- Prometheus `/metrics` (`spf_jobs_total`, `spf_queue_depth`, `spf_download_duration_seconds`), a real `/health` check, and a `warnings` endpoint for open breakers or stuck jobs.
- SQLite-backed job queue that survives restarts.

## Why not a Lidarr plugin?

Lidarr does support plugins, but they are compiled .NET assemblies loaded into Lidarr's own process, only available on Lidarr's separate "plugins" build rather than the mainline release most people run, and require a Lidarr restart to install or update. A bug in a plugin runs with the same access as Lidarr itself, including its database.

This proxy runs as its own process instead, speaking the SABnzbd and Newznab protocols Lidarr has supported for years across every build and version. That means no dependency on Lidarr's internal, less stable plugin API, no recompiling against Lidarr internals when Lidarr changes them, and a crash in the proxy stays contained to its own container instead of taking Lidarr down with it. It also runs behind gluetun like any other download client, something a plugin loaded inside Lidarr's own process cannot do.

## Standalone spotiflac-cli

Every [release](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/releases) also ships `spotiflac-cli` on its own, for `linux`/`darwin`/`windows` on `amd64`/`arm64`, built from the exact upstream commit that release was tested against. Use it if you just want SpotiFLAC downloads from the command line, with no Lidarr or proxy involved:

```bash
curl -L -o spotiflac-cli.tar.gz \
  https://github.com/fishingpvalues/spotiflac-lidarr-proxy/releases/latest/download/spotiflac-cli_<tag>_<os>_<arch>.tar.gz
tar xzf spotiflac-cli.tar.gz
./spotiflac-cli --help
```

## Running without Docker

Docker is still the recommended way to run this: it is the only distribution that bundles a matching `spotiflac-cli`. Running the server binary directly means getting `spotiflac-cli` yourself, either from a [release](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/releases) as above or built from the pinned commit in the [Dockerfile](Dockerfile).

```bash
# server: prebuilt release binary, or `go build ./cmd/server` from source (Go 1.25+)
curl -L -o spotiflac-lidarr-proxy.tar.gz \
  https://github.com/fishingpvalues/spotiflac-lidarr-proxy/releases/latest/download/spotiflac-lidarr-proxy_<tag>_<os>_<arch>.tar.gz
tar xzf spotiflac-lidarr-proxy.tar.gz

SPF_API_KEY=your-secret-key \
SPF_OUTPUT_DIR=/path/to/downloads \
SPF_SPOTIFLAC_CLI_PATH=/path/to/spotiflac-cli \
./spotiflac-lidarr-proxy serve       # -v for debug, -vv for trace (stacks like ssh -vvv)
```

Verify a downloaded archive with `sha256sum --ignore-missing -c checksums.txt`.

Testing: `go test ./... -count=1` (unit), `INTEGRATION=1 go test ./tests/integration/... -v` (spins up the real docker-compose stack). Cross-compiling like CI does: `goreleaser release --snapshot --clean --skip=publish`.

## Configuration

The essentials, as environment variables prefixed `SPF_`. Full reference: [`docs/API.md`](docs/API.md).

| Variable | Default | Description |
|----------|---------|-------------|
| `SPF_PORT` | 8484 | HTTP listen port |
| `SPF_API_KEY` | (required) | API key for Lidarr auth |
| `SPF_OUTPUT_DIR` | /downloads | FLAC output directory |
| `SPF_DEFAULT_SERVICE` | tidal | Download service |
| `SPF_DEFAULT_QUALITY` | lossless | Quality: lossless, hires |
| `SPF_FALLBACK_SERVICES` | (none) | Services to try, in order, if the primary fails |
| `SPF_MAX_CONCURRENT` | 3 | Max concurrent downloads |
| `SPF_DB_PATH` | /data/queue.db | SQLite database path |

## Security and hardening

This proxy authenticates with a single static API key, the same trust model SABnzbd, Prowlarr, and every other Lidarr download client already use. It is only as safe as the network it sits on.

We built this with the Huntarr incident in mind (early 2026, a widely-used *arr management tool shipped unauthenticated endpoints that leaked every connected app's API keys in plaintext). The API key here is compared in constant time, redacted before it reaches a log line, and every value passed to the SpotiFLAC subprocess is checked against a strict allowlist. None of that helps if the *arr stack itself is exposed to the internet; no download client can fix an exposed network.

- **Never publish this proxy's port to the internet.** Keep it on Lidarr's internal Docker network, with no `ports:` mapping beyond `localhost` unless a reverse proxy sits in front.
- **Use a reverse proxy for remote access** and terminate TLS there (Caddy/Traefik/nginx). The API key travels in the query string: unreadable over TLS, plaintext on the wire over plain HTTP.
- **Prefer a VPN over port-forwarding.** Tailscale/WireGuard into your home network beats opening a router port. For a kill switch on the streaming traffic itself, run something like Gluetun as a sidecar, the same pattern qBittorrent/Sonarr/Radarr stacks use with NordVPN/PIA.
- **Rotate `SPF_API_KEY`** if it ever leaks, and check `GET /api/sabnzbd?mode=warnings` and `/metrics` periodically.
- **Keep the image updated.** Renovate tracks dependency and base-image updates on this repo.

<details>
<summary>Example Caddy sidecar for TLS termination</summary>

```yaml
services:
  caddy:
    image: caddy:2-alpine
    ports: ["443:443"]
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile
    depends_on: [proxy]
```

```
# Caddyfile
proxy.yourdomain.com {
    reverse_proxy proxy:8484
}
```

</details>

## Troubleshooting

**Repeated download failures for one service (Tidal/Qobuz/Amazon/Deezer):** usually IP-based rate limiting, not an authentication problem. SpotiFLAC's own project confirms metadata and audio fetches can get rate-limited per IP, and recommends waiting or using a VPN.

This proxy has a built-in per-service circuit breaker: after 5 consecutive failures for one service, it stops sending it new jobs for 10 minutes and fails them immediately instead of waiting out a full timeout. Check `GET /api/sabnzbd?mode=warnings`; an open breaker shows up there with the service name and retry time. If one service's breaker keeps tripping, it is likely rate-limiting you. Either wait it out or set `SPF_FALLBACK_SERVICES` so jobs automatically try another service.

## API reference

Full route tables, response field reference, and the category system: [`docs/API.md`](docs/API.md). Machine-readable spec: [`openapi.json`](openapi.json), checked against the running server on every CI run.

## AI usage

This project was planned and implemented with AI assistance (Anthropic Claude Code). All AI-generated code goes through automated tests and manual review before merging, the same as any other contribution. Nothing here is exempt from that bar because an AI wrote the first draft.

## Legal

SpotiFLAC and this proxy are third-party tools, not affiliated with, endorsed by, or connected to Spotify, Tidal, Qobuz, Amazon Music, or any other streaming service. This project is for educational and private use only. The developer does not condone or encourage copyright infringement.

You are solely responsible for:
- Ensuring your use of this software complies with your local laws.
- Reading and following the Terms of Service of the respective platforms.
- Any legal consequences resulting from misuse of this tool.

The software is provided "as is", without warranty of any kind. The author assumes no liability for any bans, damages, or legal issues arising from its use.

**API credits (from the upstream SpotiFLAC project):** [MusicBrainz](https://musicbrainz.org), [LRCLIB](https://lrclib.net), [Songlink/Odesli](https://song.link), [Songstats](https://songstats.com), [hifi-api](https://github.com/binimum/hifi-api), [Qobuz-DL](https://github.com/QobuzDL/Qobuz-DL).

## License

[Apache License 2.0](LICENSE).
