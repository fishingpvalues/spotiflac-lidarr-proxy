# spotiflac-lidarr-proxy

[![Release](https://img.shields.io/github/v/release/fishingpvalues/spotiflac-lidarr-proxy?sort=semver)](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/releases)
[![CI](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/actions/workflows/ci.yml/badge.svg)](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)

A download client and indexer for Lidarr, backed by SpotiFLAC.

Lidarr has no concept of a streaming service. It knows Usenet indexers and
SABnzbd. This program implements both protocols, so Lidarr adds it as an
ordinary Newznab indexer and an ordinary SABnzbd client without knowing
anything unusual is happening. Behind that interface it drives SpotiFLAC,
which resolves a Spotify link to the same recording on Tidal, Qobuz, Amazon
Music or Deezer and downloads the FLAC. Spotify links are only a search key;
no account is needed on any of the five services.

Use it only for material you have the right to download - see [Legal](#legal).

## Quick start

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
environment with the SpotiFLAC module, and the browser stack that module
drives (Chromium, Node.js, Xvfb). Nothing else to install.

Put it on the same Docker network as Lidarr and give both the same downloads
volume **at the same path**, so Lidarr can import what this writes.
[`docker-compose.yml`](docker-compose.yml) is a working example.

Binaries for linux/macOS/Windows on amd64 and arm64 are attached to each
[release](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/releases)
with `checksums.txt`. Running outside a container means supplying
`spotiflac-cli` yourself.

## Configuring Lidarr

Two entries, both pointing here, both using the same `SPF_API_KEY`. If the
proxy shares a VPN sidecar's network namespace, Lidarr must address the
*sidecar's* name and published port.

**Settings > Download Clients > Add > SABnzbd**

| Field | Value |
|-------|-------|
| Host / Port | `spotiflac-proxy` / `8484` |
| API Key | your `SPF_API_KEY` |
| Category | `music` |
| URL Base, Username, Password | leave empty |

**Settings > Indexers > Add > Newznab (Custom)**

| Field | Value |
|-------|-------|
| URL | `http://spotiflac-proxy:8484` |
| API Path | `/api/newznab` |
| API Key | your `SPF_API_KEY` |
| Categories | `3000`, `3010`, `3040` |

Three things that look broken and are not:

- **Category is not optional.** Without it every job lands in Lidarr's
  default. The category is also the routing: `music-flac-24` asks for hi-res,
  `music-tidal` pins the provider, `music-qobuz-flac-16` does both.
  `mode=get_cats` lists them. Categories carry no directory on purpose -
  advertising one raises a permanent "this directory does not appear to
  exist" health error.
- **The indexer Test is red until you set `SPF_RSS_QUERY`.** This indexer
  resolves metadata for a named album; it has no "what is new", so an empty
  browse feed has nothing to answer. Directed searches - all Lidarr actually
  does - work regardless. `SPF_RSS_QUERY=new albums` makes the button green.
- **Albums are importable, tracks mostly are not.** Lidarr refuses an album
  match below 80%. An album hit therefore wins its `(artist, album)` pair and
  the track hits under it are dropped; singles are still published.

Priorities: this speaks the Usenet protocol, so it competes with your Usenet
clients. Real Usenet client 1, SpotiFLAC 5, torrents last (lower wins).

## How downloads happen

Each job goes through up to four backends, in order, first success wins.

| # | Backend | Requires | Human interaction |
|---|---------|----------|-------------------|
| 1 | SpotiFLAC Python module + bundled Chromium | writable `$HOME` | none |
| 2 | `spotiflac-cli` against a custom Tidal/Qobuz API | `SPF_TIDAL_API_URL` or a live fallback | none |
| 3 | `spotiflac-cli` with a captcha solver | `SPOTIFLAC_FSL_URL` | none |
| 4 | `spotiflac-cli` against the community tier | nothing | none with `SPF_SESSION_RENEW_CMD` |

Backend 1 handles its own authentication, needs no captcha and no third-party
mirror, which is why it is first. Backends 2-4 exist because upstream sources
fail often and independently.

Jobs live in SQLite and survive a restart. Completion is verified against the
files on disk, not assumed from a successful request. **Nothing that is merely
unavailable right now is failed:** an open circuit breaker, an announced
upstream break and a 429 all *park* the job in `Queued` - Lidarr sees a
pending download, which is the truth - rather than dumping it into failed
history where only a fresh search can recover it.

Full detail, including the measurements behind those decisions:
[`docs/OPERATIONS.md`](docs/OPERATIONS.md).

## Tidal API mirrors

Backend 2 needs a hifi-api instance. `SPF_TIDAL_API_FALLBACK_URLS` ships a
list of public ones, probed in order; the first that answers *and* can
resolve a track wins.

**Do not count on them.** Re-measured 2026-09-11 against the full published
instance list, from a VPN exit and a bare uplink alike: exactly one of ten was
alive. Five of the published hostnames no longer resolve at all. Public
instances breach Tidal's terms and get taken down, and Tidal has been blocking
the accounts behind them in bulk.

The durable answer is to run your own and point `SPF_TIDAL_API_URL` at it -
[binimum/hifi-api](https://github.com/binimum/hifi-api) ships a Dockerfile and
needs one Tidal account's refresh token. Backend 1 needs none of this and is
the one to prefer.

## Configuration

Essentials below; the full table is in [`docs/API.md`](docs/API.md).

| Variable | Default | Meaning |
|----------|---------|---------|
| `SPF_API_KEY` | **required** | Shared secret for Lidarr |
| `SPF_PORT` | 8484 | Listen port |
| `SPF_OUTPUT_DIR` | /downloads | Where finished audio is written |
| `SPF_DB_PATH` | /data/queue.db | Job queue database |
| `SPF_DEFAULT_SERVICE` | tidal | tidal, qobuz, amazon or deezer |
| `SPF_DEFAULT_QUALITY` | lossless | lossless or hires |
| `SPF_FALLBACK_SERVICES` | none | Services tried after the primary fails |
| `SPF_MAX_CONCURRENT` | 3 | Concurrent downloads |
| `SPF_JOB_TIMEOUT` | 30m | Ceiling per job |
| `SPF_RSS_QUERY` | none | Search answering the browse feed; makes the indexer Test pass |
| `SPF_TIDAL_API_URL` | none | Your own Tidal API instance |
| `SPF_LOG_LEVEL` | info | trace, debug, info, warn, error |

A service the running build cannot serve is dropped from the fallback chain
rather than tried - without the Python backend that is `deezer`, which would
otherwise burn a real fallback slot on an error identical every retry.

## Running behind a VPN

Every backend talks to third parties that log the connecting address. Route
the container through a sidecar; with
[gluetun](https://github.com/qdm12/gluetun), share its namespace:

```yaml
services:
  proxy:
    network_mode: "service:gluetun"
    depends_on:
      gluetun: { condition: service_healthy }
```

Publish ports on the gluetun service, not on this one. `depends_on` does not
survive the VPN container restarting, which takes the network with it.
WireGuard sessions expire silently - a tunnel can report healthy and pass no
packets, so check egress before suspecting this program.

## Versioning and releases

Semantic versioning, pre-1.0 on purpose: the SABnzbd surface is shaped by what
Lidarr happens to call, so it is not API-stable yet. While at `0.x`, a
breaking change bumps the minor and anything else bumps the patch.

Releases are automated. Commits follow
[Conventional Commits](https://www.conventionalcommits.org/) (enforced by the
`commit-msg` hook);
[release-please](https://github.com/googleapis/release-please) opens a release
PR that collects them, writes [`CHANGELOG.md`](CHANGELOG.md) and bumps
`version.txt`. Merging it tags `vX.Y.Z`, which is what builds and pushes the
container.

Pin a version in production. `latest` exists for convenience, not for servers.

## Troubleshooting

**`exit status 1` and `Browser failed to start within timeout`** - the
container's user cannot write `$HOME`. This is the single most common cause:

```bash
docker exec <container> sh -c 'id; ls -ld "$HOME"; touch "$HOME/.probe"'
```

**Search results show 0 tracks, 0 B, no year** and Lidarr rejects them with
"Album match is not close enough" - backend 1 is not answering searches, so
the numbers come from `spotiflac-cli`, which reports none of them. Same check
as above.

**Everything fails at once** - that is upstream or network, not
configuration. Confirm egress, then check `mode=warnings`, which reports open
breakers, park windows and pending verification.

More, including per-provider error meanings and the captcha/session paths:
[`docs/OPERATIONS.md`](docs/OPERATIONS.md).

## API reference

Routes and response fields: [`docs/API.md`](docs/API.md). The machine-readable
spec is [`openapi.json`](openapi.json), which CI checks against the running
server on every build.

## Development

```bash
make test      # go test ./...
make lint      # golangci-lint
make build     # binary into ./bin
```

Architecture and the reasoning behind the non-obvious decisions are in
[`AGENTS.md`](AGENTS.md).

## Legal

This project does not host, distribute or circumvent access to any content. It
automates a client you run yourself against services you are responsible for
using lawfully. Downloading material you have no right to is a breach of those
services' terms and may be unlawful where you live. You are responsible for
what you point it at.

## License

[Apache-2.0](LICENSE).
