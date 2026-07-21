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
- **Three download backends**, auto-selected by priority:
  1. **SpotiFLAC Python module** (recommended) — multi-service fallback (tidal→qobuz→deezer→amazon), no browser verification, 12+ services, health checks, works out of the box.
  2. **SpotiFLAC CLI** (Go binary, built-in) — headless, with optional FSL/Byparr auto-solving for community captcha.
  3. **Hifi-API adapter** — auto-detects hifi-api format proxies (`api.monochrome.tf` etc.), spawns local format translator, passes translated endpoint to SpotiFLAC CLI.
- Quality/service categories (`music-flac-24`, `music-tidal`, ...) map onto Lidarr's quality profiles.
- Multi-source Tidal API fallback chain with health-check auto-selection (8+ known public proxies, FMHY-sourced).
- Confirms each download actually completed before reporting success, not just that a request was sent.
- Per-service circuit breaker, with an optional fallback chain across Tidal/Qobuz/Amazon/Deezer.
- Prometheus `/metrics`, a real `/health` check, and a `warnings` endpoint for open breakers or stuck jobs.
- SQLite-backed job queue that survives restarts.

## Why not a Lidarr plugin?

Lidarr plugins are compiled .NET assemblies loaded into Lidarr's own process, only available on Lidarr's separate "plugins" build, and a bug in one runs with the same access as Lidarr itself. This proxy runs as its own process instead, speaking a protocol Lidarr has supported for years across every build. It survives Lidarr internals changing, can sit behind whatever VPN sidecar you already use (Gluetun or otherwise, entirely your call), and a crash in it never takes Lidarr down too.

## SpotiFLAC Python module (recommended)

The **SpotiFLAC Python module** (`pip install SpotiFLAC`) is the easiest way to get fully automated, captcha-free FLAC downloads. It has a built-in multi-service fallback chain across 12+ providers (tidal, qobuz, deezer, amazon, soundcloud, youtube, apple, pandora, joox, netease, migu, kuwo) and handles authentication internally — **no account, no login, no browser verification required**. The proxy ships a wrapper script ([`scripts/spotiflac-py-wrapper.py`](scripts/spotiflac-py-wrapper.py)) that calls the Python module and emits the same JSON progress format as the CLI.

### Setup

```dockerfile
# In your Dockerfile (add to the existing multi-stage build):
RUN apk add --no-cache python3 py3-pip && \
    python3 -m venv /venv && \
    /venv/bin/pip install --no-cache-dir SpotiFLAC requests
COPY scripts/spotiflac-py-wrapper.py /app/scripts/
```

```bash
# Env vars:
SPF_SPOTIFLAC_PYTHON_PATH=/app/scripts/spotiflac-py-wrapper.py
SPF_SPOTIFLAC_PYTHON_VENV=/venv/bin/python3
```

When both are set, the proxy uses the Python module instead of the Go CLI. The Python subprocess inherits `HTTP_PROXY`/`HTTPS_PROXY` from the proxy — **always set these to route through VPN** (see below).

### How it works

1. Lidarr sends Spotify URL to proxy (SABnzbd addurl)
2. Proxy spawns Python wrapper with `--service tidal,qobuz,deezer,amazon`
3. Python module fetches track metadata from Spotify's public API (ISRC, title, artist)
4. Matches ISRC against Tidal → Qobuz → Deezer → Amazon (in order, first working wins)
5. Downloads FLAC directly from the matching service's community proxy
6. Emits JSON progress events → proxy updates queue → Lidarr imports

**Spotify is only queried for metadata** (track name, ISRC). The audio comes from Tidal/Qobuz/Deezer/Amazon community proxies — never from Spotify's servers.

### VPN requirement

> [!IMPORTANT]
> Always route through VPN. The Python module connects to community proxies that serve copyrighted audio. Without VPN, your real IP is exposed to these services and to copyright enforcement. Set `HTTP_PROXY` and `HTTPS_PROXY` to your gluetun/OpenVPN/WireGuard HTTP proxy.

```yaml
# docker-compose.yml
environment:
  - HTTP_PROXY=http://gluetun:8888
  - HTTPS_PROXY=http://gluetun:8888
  - SPF_SPOTIFLAC_PYTHON_PATH=/app/scripts/spotiflac-py-wrapper.py
  - SPF_SPOTIFLAC_PYTHON_VENV=/venv/bin/python3
```

### CLI only, or without Docker

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

## Captcha Solving (Byparr / FlareSolverr)

When using the **SpotiFLAC CLI** backend (not the Python module), community verification requires solving a Cloudflare Turnstile. The proxy integrates with **Byparr** (a FlareSolverr-compatible proxy) to attempt this automatically.

> **Note:** The Python module (recommended) does NOT need this — it uses alternative auth methods that skip browser verification entirely. This section only applies if you're using the CLI backend without a custom API URL.

### How It Works

1. SpotiFLAC CLI emits `verification_required` JSON event with a challenge URL
2. Proxy's FSL integration sends the challenge URL to Byparr's headless browser
3. Byparr loads the challenge page, auto-solves the Turnstile widget
4. The challenge page redirects to the proxy's `/api/verify-relay` endpoint with a grant
5. Proxy forwards the grant to SpotiFLAC's local callback server
6. SpotiFLAC exchanges the grant for a community session — download proceeds

### Configuration

| Env Variable | Default | Description |
|-------------|---------|-------------|
| `SPOTIFLAC_FSL_URL` | (unset) | Byparr/FlareSolverr API endpoint (e.g. `http://byparr:8191`) |
| `SPOTIFLAC_ADDRESS` | auto-detected | This container's routable IP for the Turnstile callback reachable by Byparr's browser |

> **Known limitation:** The `verify.spotbye.qzz.io` challenge page is behind Cloudflare and may block headless browsers (returns HTTP 400). In testing, Byparr correctly receives the challenge URL but the verification page rejects the headless browser. Use the Python module or custom API URLs to bypass this entirely.

## Multi-source Tidal API fallback

When using the CLI backend with `SPF_TIDAL_API_URL`, the proxy health-checks all configured API URLs before each download and auto-selects the first working one. If the primary URL fails, it falls back through `SPF_TIDAL_API_FALLBACK_URLS` (comma-separated list) before giving up.

### Hifi-API format adapter

Many community Tidal proxies (`api.monochrome.tf`, `lossless.wtf`, etc.) use a manifest-based response format (hifi-api) instead of the direct URL format SpotiFLAC expects. The proxy **auto-detects** hifi-api instances and spawns a local format adapter that translates base64-encoded BTS/MPD manifests into direct download URLs.

No configuration needed — detection is automatic via fingerprinting the root endpoint.

### Known public proxies

| URL | Format | Status (via gluetun) |
|-----|--------|----------------------|
| `api.monochrome.tf` | hifi-api | Alive, upstream token dead |
| `lossless.wtf` | hifi-api | Alive (monochrome mirror) |
| `wolf/maus/vogel/katze/hund.qqdl.site` | community | DNS resolves, Cloudflare block |
| `squid.wtf` | web tool | Alive |
| `doubledouble.top` | web tool | Alive |
| `antra.hoshi.cfd` | web tool | Alive |
| `arcod.xyz` | web tool | Alive |
| `vdwn.cloud` | web tool | Alive |
| `imov.life` | web tool | Alive |
| `lucida.to` | web tool | Cloudflare 403 |
| `hifi.geeked.wtf` | unknown | Dead |
| `tidal.kinoplus.online` | unknown | Dead |

Sources: [FMHY](https://fmhy.net/audiopiracyguide), [tidaloader](https://github.com/RayZ3R0/tidaloader), [spofree](https://github.com/redretep/spofree). These URLs change frequently — set your own in `SPF_TIDAL_API_FALLBACK_URLS`.

### Alternative: Custom API URLs (No Captcha Needed)

Set `SPF_TIDAL_API_URL` and/or `SPF_QOBUZ_API_URL` to point at self-hosted hifi-api instances. When these are configured, SpotiFLAC bypasses the community tier entirely — no Turnstile, no verification.

## Configuration

Environment variables, all prefixed `SPF_`. Full reference: [`docs/API.md`](docs/API.md).

| Variable | Default | Description |
|----------|---------|-------------|
| `SPF_PORT` | 8484 | HTTP listen port |
| `SPF_API_KEY` | (required) | API key for Lidarr auth |
| `SPF_OUTPUT_DIR` | /downloads | FLAC output directory |
| `SPF_SPOTIFLAC_CLI_PATH` | /usr/local/bin/spotiflac-cli | Path to the spotiflac-cli binary (Go CLI backend) |
| `SPF_SPOTIFLAC_PYTHON_PATH` | (none) | Path to `spotiflac-py-wrapper.py` — enables Python module backend (recommended) |
| `SPF_SPOTIFLAC_PYTHON_VENV` | (none) | Path to venv Python binary, e.g. `/venv/bin/python3` |
| `SPF_DEFAULT_SERVICE` | tidal | Download service |
| `SPF_DEFAULT_QUALITY` | lossless | Quality: lossless, hires |
| `SPF_FALLBACK_SERVICES` | (none) | Services to try, in order, if the primary fails |
| `SPF_MAX_CONCURRENT` | 3 | Max concurrent downloads |
| `SPF_JOB_TIMEOUT` | 30m | How long a download can run before it is considered stuck |
| `SPF_DB_PATH` | /data/queue.db | SQLite database path |
| `SPF_LOG_LEVEL` | info | Log verbosity: trace, debug, info, warn, error |
| `SPF_HISTORY_RETENTION_COUNT` | 500 | Completed/failed jobs to keep in history |
| `SPF_TIDAL_API_URL` | (none) | Custom Tidal API instance; skips community verification entirely |
| `SPF_QOBUZ_API_URL` | (none) | Custom Qobuz API instance; skips community verification entirely |
| `SPF_TIDAL_API_FALLBACK_URLS` | 6 known proxies | Comma-separated Tidal API URLs, tried in order. Health-checked before each download |
| `SPF_VERIFY_RELAY_URL` | (none) | This proxy's own reachable `/verify/callback` URL for manual browser verification |
| `SPF_VERIFY_NOTIFY_URL` | (none) | POSTs verification link here. Works with ntfy, Gotify, any POST-accepting URL |
| `SPF_VERIFY_NOTIFY_TITLE` | SpotiFLAC verification needed | Sent as `Title` header alongside `SPF_VERIFY_NOTIFY_URL` |

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

Only applies to the **Go CLI backend**. The Python module (recommended) does not need this. Options, in order of reliability:

1. **Use the Python module** (recommended). Set `SPF_SPOTIFLAC_PYTHON_PATH` + `SPF_SPOTIFLAC_PYTHON_VENV`. The Python module uses alternative auth methods that skip browser verification entirely. See [SpotiFLAC Python module](#spotiflac-python-module-recommended) above.
2. Set `SPF_TIDAL_API_URL` and/or `SPF_QOBUZ_API_URL` to a custom Tidal/Qobuz API instance (self-hosted, with your own account). Both skip the community tier and its verification entirely.
3. Run the `ghcr.io/fishingpvalues/spotiflac-lidarr-proxy:latest-gui` image alongside this one, sharing the same app-data volume. SpotiFLAC desktop app behind noVNC at port 6901. Complete verification once in a real browser, session persists on shared volume. See [`docker-compose.gui.yml`](docker-compose.gui.yml).
4. Set `SPF_VERIFY_RELAY_URL` to this proxy's reachable `/verify/callback` URL, open the link from `mode=warnings` in any browser. Mechanically works but the challenge page may not complete reliably.
5. Run SpotiFLAC desktop once on any machine with a browser, copy the session file to `/home/spotiflac/.spotiflac` in the container.

## API reference

Full route tables and response fields: [`docs/API.md`](docs/API.md). Machine-readable spec: [`openapi.json`](openapi.json), checked against the running server on every CI run.

## AI usage

This project was planned and implemented with AI assistance (Anthropic Claude Code). All AI-generated code goes through the same automated tests and manual review as any other contribution.

## Legal

SpotiFLAC and this proxy are third-party tools, not affiliated with Spotify, Tidal, Qobuz, Amazon Music, or any other streaming service. For educational and private use only. You are responsible for complying with your local laws and the Terms of Service of the respective platforms. Provided "as is", without warranty of any kind; the author assumes no liability for any bans, damages, or legal issues arising from its use.

**API credits (from the upstream SpotiFLAC project):** [MusicBrainz](https://musicbrainz.org), [LRCLIB](https://lrclib.net), [Songlink/Odesli](https://song.link), [Songstats](https://songstats.com), [hifi-api](https://github.com/binimum/hifi-api), [Qobuz-DL](https://github.com/QobuzDL/Qobuz-DL).

## License

[Apache License 2.0](LICENSE).
