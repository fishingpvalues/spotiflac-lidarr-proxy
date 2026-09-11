# spotiflac-lidarr-proxy

[![Release](https://img.shields.io/github/v/release/fishingpvalues/spotiflac-lidarr-proxy?sort=semver)](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/releases)
[![CI](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/actions/workflows/ci.yml/badge.svg)](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)

`spotiflac-lidarr-proxy` makes [SpotiFLAC](https://github.com/fishingpvalues/SpotiFLAC)
usable from [Lidarr](https://lidarr.audio) by pretending to be a Usenet stack.

Lidarr has no concept of a streaming service; it knows Newznab indexers and
SABnzbd clients. This implements both protocols, so Lidarr adds it as an
ordinary indexer and an ordinary download client. Behind that, SpotiFLAC
resolves a Spotify link to the same recording on Tidal, Qobuz, Amazon Music or
Deezer and downloads the FLAC. Spotify links are a search key only; no account
is needed on any of the five services.

Download only what you have the right to download. See [Legal](#legal).

### Contents

[Install](#install) · [Lidarr setup](#lidarr-setup) · [How it works](#how-it-works) ·
[Tidal mirrors](#tidal-mirrors) · [Configuration](#configuration) ·
[Behind a VPN](#behind-a-vpn) · [Versioning](#versioning) ·
[Troubleshooting](#troubleshooting) · [Contributing](#contributing)

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

The image is self-contained: the proxy, a matching `spotiflac-cli`, a Python
environment with the SpotiFLAC module, and the browser stack it drives
(Chromium, Node.js, Xvfb).

Put it on the same Docker network as Lidarr and give both the same downloads
volume **at the same path**, or Lidarr cannot import what this writes.
[`docker-compose.yml`](docker-compose.yml) is a working example.

Binaries for Linux, macOS and Windows on amd64 and arm64, plus
`checksums.txt`, are attached to every
[release](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/releases).
Running outside a container means supplying `spotiflac-cli` yourself.

## Lidarr setup

Two entries, both pointing here, both using the same `SPF_API_KEY`. If the
proxy shares a VPN sidecar's network namespace, address the *sidecar's* name
and published port.

**Settings › Download Clients › Add › SABnzbd**

| Field | Value |
|-------|-------|
| Host / Port | `spotiflac-proxy` / `8484` |
| API Key | `$SPF_API_KEY` |
| Category | `music` |
| URL Base, Username, Password | empty |

**Settings › Indexers › Add › Newznab (Custom)**

| Field | Value |
|-------|-------|
| URL | `http://spotiflac-proxy:8484` |
| API Path | `/api/newznab` |
| API Key | `$SPF_API_KEY` |
| Categories | `3000`, `3010`, `3040` |

Three things that look broken and are not:

**Category is required.** Without it every job lands in Lidarr's default. The
category is also the routing: `music-flac-24` requests hi-res, `music-tidal`
pins the provider, `music-qobuz-flac-16` does both; `mode=get_cats` lists them
all. Categories carry no directory on purpose - advertising one raises a
permanent "this directory does not appear to exist" health error.

**The indexer Test is red until `SPF_RSS_QUERY` is set.** This indexer resolves
metadata for a named album; it has no "what is new", so an empty browse feed
has nothing to answer. Directed searches - all Lidarr actually does - work
either way. `SPF_RSS_QUERY="new albums"` turns the button green.

**Albums import, most tracks do not.** Lidarr rejects an album match below 80%,
so an album hit wins its `(artist, album)` pair and the track hits beneath it
are dropped. Singles are still published.

Priorities: this speaks the Usenet protocol and competes with your Usenet
clients. Real Usenet client `1`, this `5`, torrents last - lower wins.

## How it works

Each job tries up to four backends in order; first success wins.

| # | Backend | Requires | Interaction |
|---|---------|----------|-------------|
| 1 | SpotiFLAC Python module + bundled Chromium | writable `$HOME` | none |
| 2 | `spotiflac-cli` against a custom Tidal/Qobuz API | `SPF_TIDAL_API_URL` or a live mirror | none |
| 3 | `spotiflac-cli` with a captcha solver | `SPOTIFLAC_FSL_URL` | none |
| 4 | `spotiflac-cli` against the community tier | nothing | none with `SPF_SESSION_RENEW_CMD` |

Backend 1 authenticates itself, needs no captcha and no third-party mirror,
which is why it is first. The rest exist because upstream sources fail often
and independently.

Jobs live in SQLite and survive restarts. Completion is verified against the
files on disk, not assumed from a successful request. **Nothing merely
unavailable is failed:** an open circuit breaker, an announced upstream break
and a 429 all *park* the job in `Queued` - which is what it is - instead of
dumping it into Lidarr's failed history, where only a fresh search recovers it.

Detail and the measurements behind those choices:
[`docs/OPERATIONS.md`](docs/OPERATIONS.md).

## Tidal mirrors

Backend 2 needs a hifi-api instance. `SPF_TIDAL_API_FALLBACK_URLS` ships a list
of public ones, probed in order; the first that answers *and* resolves a track
wins.

Do not rely on them. Measured 2026-09-11 against the full published instance
list, from a VPN exit and a bare uplink alike: **one of ten was alive**, and
five of the published hostnames no longer resolve. Public instances breach
Tidal's terms and are taken down, and Tidal has been blocking the accounts
behind them in bulk.

Run your own and point `SPF_TIDAL_API_URL` at it:
[binimum/hifi-api](https://github.com/binimum/hifi-api) ships a Dockerfile and
needs one Tidal account's refresh token. Backend 1 needs none of this.

## Configuration

Essentials; full table in [`docs/API.md`](docs/API.md).

| Variable | Default | Meaning |
|----------|---------|---------|
| `SPF_API_KEY` | **required** | Shared secret for Lidarr |
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

A service the running build cannot serve is dropped from the fallback chain
rather than tried. Without the Python backend that means `deezer`, which would
otherwise burn a fallback slot on an error identical every retry.

## Behind a VPN

Every backend talks to third parties that log the connecting address. Route the
container through a sidecar; with [gluetun](https://github.com/qdm12/gluetun),
share its namespace:

```yaml
services:
  proxy:
    network_mode: "service:gluetun"
    depends_on:
      gluetun: { condition: service_healthy }
```

Publish ports on the gluetun service, not this one. `depends_on` does not
survive the VPN container restarting, which takes the network with it.
WireGuard sessions expire silently - a tunnel can report healthy and pass no
packets - so check egress before suspecting this program.

## Versioning

Semantic versioning, pre-1.0 deliberately: the SABnzbd surface is shaped by
what Lidarr happens to call, so it is not API-stable yet. While at `0.x`, a
breaking change bumps the minor and everything else bumps the patch.

Releases are automated. Commits follow
[Conventional Commits](https://www.conventionalcommits.org/);
[release-please](https://github.com/googleapis/release-please) collects them
into a release PR, writes [`CHANGELOG.md`](CHANGELOG.md) and bumps
`version.txt`. Merging tags `vX.Y.Z`, which builds and pushes the container.

Pin a version in production. `latest` is for convenience, not for servers.

## Troubleshooting

**`exit status 1`, log says `Browser failed to start within timeout`.** The
container's user cannot write `$HOME` - by far the most common cause:

```bash
docker exec <container> sh -c 'id; ls -ld "$HOME"; touch "$HOME/.probe"'
```

**Search results show 0 tracks, 0 B, no year**, and Lidarr rejects them with
"Album match is not close enough". Backend 1 is not answering searches, so the
numbers come from `spotiflac-cli`, which reports none of them. Same check.

**Everything fails at once.** Upstream or network, not configuration. Confirm
egress, then read `mode=warnings` - it reports open breakers, park windows and
pending verification.

Per-provider error meanings and the captcha and session paths:
[`docs/OPERATIONS.md`](docs/OPERATIONS.md).

## API

Routes and response fields: [`docs/API.md`](docs/API.md). The machine-readable
spec is [`openapi.json`](openapi.json), checked against the running server on
every build.

## Development

```bash
make test    # go test ./...
make lint    # golangci-lint
make build   # binary into ./bin
```

Architecture and the reasoning behind the non-obvious decisions live in
[`AGENTS.md`](AGENTS.md).

## Contributing

Small fixes: open a PR. Anything larger: open an issue first - this project
says no to a fair amount, and [`CONTRIBUTING.md`](CONTRIBUTING.md) says what
and why.

Security issues go through
[private advisories](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/security/advisories/new),
never public issues. [`SECURITY.md`](SECURITY.md) states the threat model
plainly: a homelab service with a shared-secret key over plain HTTP, assumed
not to be exposed to the internet.

## Legal

This project does not host, distribute or circumvent access to any content. It
automates a client you run yourself against services you are responsible for
using lawfully. Downloading material you have no right to breaches those
services' terms and may be unlawful where you live. You are responsible for
what you point it at.

## License

[Apache-2.0](LICENSE).
