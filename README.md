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

This proxy implements the SABnzbd download client API and the Newznab indexer API, so Lidarr treats it like an ordinary Usenet setup. Underneath, it drives a headless SpotiFLAC CLI to pull FLAC and hi-res audio from Tidal, Qobuz, Amazon Music, and Deezer, using Spotify links as the search key, no account or login required for any of the four. The published Docker image bundles a matching `spotiflac-cli` build, so this one container is all you need.

## Getting started

```bash
git clone https://github.com/fishingpvalues/spotiflac-lidarr-proxy.git
cd spotiflac-lidarr-proxy
cp .env.example .env
# edit .env and set SPF_API_KEY to a random string
docker compose up -d
```

This starts the proxy next to a Lidarr container on the same Docker network, sharing a `/downloads` volume. See [`docker-compose.yml`](docker-compose.yml) for the full list of settings, or use a prebuilt image directly:

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
```

### Lidarr setup

1. **Download Client:** Settings -> Download Clients -> Add -> SABnzbd
   - Host: `proxy`, Port: `8484`, URL Base: leave empty
   - API Key: your `SPF_API_KEY` value, Category: `music`

2. **Indexer:** Settings -> Indexers -> Add -> Newznab
   - URL: `http://proxy:8484/api/newznab`
   - API Key: your `SPF_API_KEY` value, Categories: 3010, 3040

## Features

- Speaks both halves of Lidarr's protocol: SABnzbd (download client) and Newznab (indexer).
- Quality/service categories (`music-flac-24`, `music-tidal`, ...) map onto Lidarr's quality profiles.
- Confirms each download actually completed before reporting success, not just that a request was sent.
- Per-service circuit breaker, with an optional fallback chain across Tidal/Qobuz/Amazon/Deezer.
- Prometheus `/metrics`, a real `/health` check, and a `warnings` endpoint for open breakers or stuck jobs.
- SQLite-backed job queue that survives restarts.

## Why not a Lidarr plugin?

Lidarr plugins are compiled .NET assemblies loaded into Lidarr's own process, only available on Lidarr's separate "plugins" build, and a bug in one runs with the same access as Lidarr itself. This proxy runs as its own process instead, speaking a protocol Lidarr has supported for years across every build. It survives Lidarr internals changing, can sit behind whatever VPN sidecar you already use (Gluetun or otherwise, entirely your call), and a crash in it never takes Lidarr down too.

## CLI only, or without Docker

Every [release](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/releases) ships three things: the proxy binary, a standalone `spotiflac-cli` binary, and a `checksums.txt` to verify them, for `linux`/`darwin`/`windows` on `amd64`/`arm64`. If you just want SpotiFLAC downloads from the command line with no Lidarr involved, grab `spotiflac-cli` on its own:

```bash
curl -L -o spotiflac-cli.tar.gz \
  https://github.com/fishingpvalues/spotiflac-lidarr-proxy/releases/latest/download/spotiflac-cli_<tag>_<os>_<arch>.tar.gz
tar xzf spotiflac-cli.tar.gz && ./spotiflac-cli --help
```

To run the proxy itself without Docker, download `spotiflac-lidarr-proxy_<tag>_<os>_<arch>.tar.gz` the same way (or `go build ./cmd/server`), then point it at a `spotiflac-cli`:

```bash
SPF_API_KEY=your-secret-key SPF_OUTPUT_DIR=/path/to/downloads \
SPF_SPOTIFLAC_CLI_PATH=/path/to/spotiflac-cli \
./spotiflac-lidarr-proxy serve       # -v for debug, -vv for trace
```

Testing: `go test ./... -count=1` (unit), `INTEGRATION=1 go test ./tests/integration/... -v` (real docker-compose stack).

## Configuration

Environment variables, all prefixed `SPF_`. Full reference: [`docs/API.md`](docs/API.md).

| Variable | Default | Description |
|----------|---------|-------------|
| `SPF_PORT` | 8484 | HTTP listen port |
| `SPF_API_KEY` | (required) | API key for Lidarr auth |
| `SPF_OUTPUT_DIR` | /downloads | FLAC output directory |
| `SPF_SPOTIFLAC_CLI_PATH` | /usr/local/bin/spotiflac-cli | Path to the spotiflac-cli binary |
| `SPF_DEFAULT_SERVICE` | tidal | Download service |
| `SPF_DEFAULT_QUALITY` | lossless | Quality: lossless, hires |
| `SPF_FALLBACK_SERVICES` | (none) | Services to try, in order, if the primary fails |
| `SPF_MAX_CONCURRENT` | 3 | Max concurrent downloads |
| `SPF_JOB_TIMEOUT` | 30m | How long a download can run before it is considered stuck |
| `SPF_DB_PATH` | /data/queue.db | SQLite database path |
| `SPF_LOG_LEVEL` | info | Log verbosity: trace, debug, info, warn, error |
| `SPF_HISTORY_RETENTION_COUNT` | 500 | Completed/failed jobs to keep in history |
| `SPF_VERIFY_RELAY_URL` | (none) | This proxy's own reachable `/verify/callback` URL, e.g. `https://spotiflac.example.com/verify/callback`. Lets Tidal/Qobuz/Amazon's one-time community verification be completed from a browser on a different machine; see [Troubleshooting](#troubleshooting). |
| `SPF_TIDAL_API_URL` | (none) | Custom Tidal API instance; skips the community verification tier entirely if set |
| `SPF_QOBUZ_API_URL` | (none) | Custom Qobuz API instance; skips the community verification tier entirely if set |

## Security

This proxy authenticates with a single static API key, the same trust model SABnzbd and every other Lidarr download client already use. It is only as safe as the network it sits on: the API key is compared in constant time, redacted from logs, and every value passed to the SpotiFLAC subprocess is checked against a strict allowlist, but none of that helps if the *arr stack itself is exposed to the internet.

- **Never publish this proxy's port to the internet.** Keep it on Lidarr's internal Docker network.
- **Use a reverse proxy** (Caddy/Traefik/nginx) and terminate TLS there for remote access.
- **Prefer a VPN over port-forwarding.** Tailscale/WireGuard beats opening a router port.
- **Rotate `SPF_API_KEY`** if it ever leaks, and check `GET /api/sabnzbd?mode=warnings` periodically.
- **Keep the image updated.** Renovate tracks dependency and base-image updates on this repo.

## Troubleshooting

Repeated failures for one service (Tidal/Qobuz/Amazon/Deezer) are usually IP-based rate limiting, not an auth problem. The built-in circuit breaker stops sending jobs to a service after 5 consecutive failures for 10 minutes; check `GET /api/sabnzbd?mode=warnings` for open breakers, or set `SPF_FALLBACK_SERVICES` so jobs try another service automatically.

### "browser integration is not ready" / one-time community verification

By default, Tidal, Qobuz, and Amazon downloads go through a shared community API that needs a one-time interactive browser verification. On a headless server there is no browser to complete it. Three options, roughly in order of how little manual effort they need:

1. Set `SPF_TIDAL_API_URL` and/or `SPF_QOBUZ_API_URL` to a custom Tidal/Qobuz API instance (self-hosted or a known public one). Both are tried before the community tier and, if reachable, skip it (and its verification requirement) entirely. Amazon has no equivalent; it always uses the community tier.
2. Set `SPF_VERIFY_RELAY_URL` to this proxy's own reachable `/verify/callback` URL. When verification is needed, `GET /api/sabnzbd?mode=warnings` shows a link; open it in any browser to complete verification, and it relays back automatically. The resulting session lasts until the remote service expires it, then repeats.
3. Run the SpotiFLAC desktop app once on a machine with a browser, complete verification there, and copy the resulting session file into this proxy's persistent app-data volume (`/home/spotiflac/.spotiflac` in the container).

## API reference

Full route tables and response fields: [`docs/API.md`](docs/API.md). Machine-readable spec: [`openapi.json`](openapi.json), checked against the running server on every CI run.

## AI usage

This project was planned and implemented with AI assistance (Anthropic Claude Code). All AI-generated code goes through the same automated tests and manual review as any other contribution.

## Legal

SpotiFLAC and this proxy are third-party tools, not affiliated with Spotify, Tidal, Qobuz, Amazon Music, or any other streaming service. For educational and private use only. You are responsible for complying with your local laws and the Terms of Service of the respective platforms. Provided "as is", without warranty of any kind; the author assumes no liability for any bans, damages, or legal issues arising from its use.

**API credits (from the upstream SpotiFLAC project):** [MusicBrainz](https://musicbrainz.org), [LRCLIB](https://lrclib.net), [Songlink/Odesli](https://song.link), [Songstats](https://songstats.com), [hifi-api](https://github.com/binimum/hifi-api), [Qobuz-DL](https://github.com/QobuzDL/Qobuz-DL).

## License

[Apache License 2.0](LICENSE).
