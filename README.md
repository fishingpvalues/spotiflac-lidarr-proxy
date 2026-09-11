# spotiflac-lidarr-proxy

[![Release](https://img.shields.io/github/v/release/fishingpvalues/spotiflac-lidarr-proxy?sort=semver)](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/releases)
[![CI](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/actions/workflows/ci.yml/badge.svg)](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)

Use [SpotiFLAC](https://github.com/fishingpvalues/SpotiFLAC) from
[Lidarr](https://lidarr.audio).

Lidarr speaks to Newznab indexers and SABnzbd clients. This implements both,
so Lidarr treats it as an ordinary Usenet setup. Behind that, SpotiFLAC
resolves a Spotify link to the same recording on Tidal, Qobuz, Amazon Music or
Deezer and downloads the FLAC. Spotify links are a search key; no account is
needed on any of the five services.

Only download what you have the right to download. See [Legal](#legal).

## Install

```bash
docker run -d \
  -p 8484:8484 \
  --shm-size=512m \
  -e SPF_API_KEY=$(openssl rand -hex 16) \
  -v /srv/downloads:/downloads \
  -v spotiflac-data:/data \
  ghcr.io/fishingpvalues/spotiflac-lidarr-proxy:latest
```

The image contains the proxy, a matching `spotiflac-cli`, a Python environment
with the SpotiFLAC module, and the browser stack it drives (Chromium, Node.js,
Xvfb).

Run it on the same Docker network as Lidarr, with the same downloads volume
mounted at the same path in both. See [`docker-compose.yml`](docker-compose.yml).

Binaries for Linux, macOS and Windows on amd64 and arm64 are attached to each
[release](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/releases)
with `checksums.txt`. Outside a container you supply `spotiflac-cli` yourself.

## Lidarr

Two entries, both using the same `SPF_API_KEY`. If the proxy shares a VPN
sidecar's network namespace, use the sidecar's name and published port.

Download client — Settings › Download Clients › Add › SABnzbd:

| Field | Value |
|-------|-------|
| Host / Port | `spotiflac-proxy` / `8484` |
| API Key | `$SPF_API_KEY` |
| Category | `music` |
| URL Base, Username, Password | empty |

Indexer — Settings › Indexers › Add › Newznab (Custom):

| Field | Value |
|-------|-------|
| URL | `http://spotiflac-proxy:8484` |
| API Path | `/api/newznab` |
| API Key | `$SPF_API_KEY` |
| Categories | `3000`, `3010`, `3040` |

### Categories

Set one. Without it every job lands in Lidarr's default.

The category also selects service and quality: `music-flac-24` requests
hi-res, `music-tidal` pins the provider, `music-qobuz-flac-16` does both.
`mode=get_cats` lists them.

Categories carry no directory. Advertising one makes Lidarr raise a permanent
"this directory does not appear to exist" health error.

### Indexer Test is red until `SPF_RSS_QUERY` is set

The indexer resolves metadata for a named album and has no browse feed, so an
empty `t=music` query returns nothing and Lidarr reports the Test as failed.
Directed searches work either way. Set `SPF_RSS_QUERY="new albums"` for a
green Test.

### Albums, not tracks

Lidarr rejects an album match below 80%. An album hit wins its
`(artist, album)` pair and the track hits under it are dropped. Singles are
still published.

### Priorities

This speaks the Usenet protocol and competes with your Usenet clients. Lower
wins: real Usenet client `1`, this `5`, torrents last.

## How it works

Four backends, tried in order, first success wins.

| # | Backend | Requires | Interaction |
|---|---------|----------|-------------|
| 1 | SpotiFLAC Python module + bundled Chromium | writable `$HOME` | none |
| 2 | `spotiflac-cli` against a custom Tidal/Qobuz API | `SPF_TIDAL_API_URL` or a live mirror | none |
| 3 | `spotiflac-cli` with a captcha solver | `SPOTIFLAC_FSL_URL` | none |
| 4 | `spotiflac-cli` against the community tier | nothing | none with `SPF_SESSION_RENEW_CMD` |

Backend 1 authenticates itself and needs no captcha or third-party mirror.
The others exist because upstream sources fail often and independently.

Jobs are stored in SQLite and survive restarts. Completion is checked against
the files on disk. An open circuit breaker, an announced upstream break and a
429 all park the job in `Queued` rather than failing it, because a failed job
leaves Lidarr's history and only a fresh search brings it back.

See [`docs/OPERATIONS.md`](docs/OPERATIONS.md).

## Tidal mirrors

Backend 2 needs a hifi-api instance. `SPF_TIDAL_API_FALLBACK_URLS` ships a
list of public ones, probed in order; the first that answers and resolves a
track is used.

Public instances are unreliable. As of 2026-09-11, one of the ten published
instances was reachable and five of the hostnames no longer resolve. They
breach Tidal's terms and get taken down, and Tidal blocks the accounts behind
them.

For a stable setup, run [binimum/hifi-api](https://github.com/binimum/hifi-api)
yourself and set `SPF_TIDAL_API_URL`. It ships a Dockerfile and needs one
Tidal account's refresh token. Backend 1 needs none of this.

## Configuration

Full table in [`docs/API.md`](docs/API.md).

| Variable | Default | Meaning |
|----------|---------|---------|
| `SPF_API_KEY` | required | Shared secret for Lidarr |
| `SPF_PORT` | `8484` | Listen port |
| `SPF_OUTPUT_DIR` | `/downloads` | Where finished audio is written |
| `SPF_DB_PATH` | `/data/queue.db` | Job queue database |
| `SPF_DEFAULT_SERVICE` | `tidal` | `tidal`, `qobuz`, `amazon`, `deezer` |
| `SPF_DEFAULT_QUALITY` | `lossless` | `lossless` or `hires` |
| `SPF_FALLBACK_SERVICES` | none | Services tried after the primary fails |
| `SPF_MAX_CONCURRENT` | `3` | Concurrent downloads |
| `SPF_JOB_TIMEOUT` | `30m` | Ceiling per job |
| `SPF_RSS_QUERY` | none | Search answering the browse feed |
| `SPF_TIDAL_API_URL` | none | Your own Tidal API instance |
| `SPF_LOG_LEVEL` | `info` | `trace`, `debug`, `info`, `warn`, `error` |

Services the running build cannot serve are dropped from the fallback chain.
Without the Python backend that is `deezer`.

## VPN

Every backend contacts third parties that log the connecting address. Route
the container through a sidecar. With
[gluetun](https://github.com/qdm12/gluetun), share its namespace:

```yaml
services:
  proxy:
    network_mode: "service:gluetun"
    depends_on:
      gluetun: { condition: service_healthy }
```

Publish ports on the gluetun service, not this one. `depends_on` does not
survive the VPN container restarting. WireGuard tunnels can report healthy
while passing no packets, so check egress before suspecting this program.

## Versioning

Semantic versioning, pre-1.0: the SABnzbd surface follows what Lidarr calls
and is not stable yet. At `0.x` a breaking change bumps the minor, everything
else the patch.

Commits follow [Conventional Commits](https://www.conventionalcommits.org/).
[release-please](https://github.com/googleapis/release-please) collects them
into a release PR, writes [`CHANGELOG.md`](CHANGELOG.md) and bumps
`version.txt`. Merging tags `vX.Y.Z` and builds the container.

Pin a version in production; `latest` moves.

## Troubleshooting

`exit status 1` with `Browser failed to start within timeout` — the container
user cannot write `$HOME`:

```bash
docker exec <container> sh -c 'id; ls -ld "$HOME"; touch "$HOME/.probe"'
```

Search results show 0 tracks, 0 B and no year, and Lidarr rejects them with
"Album match is not close enough" — backend 1 is not answering searches, so
the values come from `spotiflac-cli`, which does not report them. Same check
as above.

Everything fails at once — check egress, then `mode=warnings`, which lists
open breakers, park windows and pending verification.

Per-provider errors and the captcha and session paths are in
[`docs/OPERATIONS.md`](docs/OPERATIONS.md).

## API

Routes and response fields: [`docs/API.md`](docs/API.md).
[`openapi.json`](openapi.json) is checked against the running server on every
build.

## Development

```bash
make test    # go test ./...
make lint    # golangci-lint
make build   # binary into ./bin
```

Architecture notes are in [`AGENTS.md`](AGENTS.md).

## Contributing

Small fixes: open a PR. Larger changes: open an issue first. See
[`CONTRIBUTING.md`](CONTRIBUTING.md).

Report security issues through
[private advisories](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/security/advisories/new).
[`SECURITY.md`](SECURITY.md) describes the threat model: a homelab service
with a shared-secret key over plain HTTP, not intended to face the internet.

## Legal

This project does not host, distribute or circumvent access to any content. It
automates a client you run against services you are responsible for using
lawfully. Downloading material you have no right to breaches those services'
terms and may be unlawful where you live.

## License

[Apache-2.0](LICENSE).
