# spotiflac-lidarr-proxy

A download client and indexer for Lidarr, backed by SpotiFLAC.

Lidarr has no concept of a streaming service. It knows how to talk to Usenet
indexers and to SABnzbd. This program implements both of those protocols, so
Lidarr can add it as an ordinary Newznab indexer and an ordinary SABnzbd
download client without knowing anything unusual is happening. Behind that
interface it drives SpotiFLAC, which resolves a Spotify link to the same
recording on Tidal, Qobuz, Amazon Music or Deezer and downloads the FLAC.
Spotify links are used only as a search key; no account is needed on any of
the five services.

Use it only for material you have the right to download. See [Legal](#legal).

## Installation

The container image contains everything the program needs: the proxy, a
matching `spotiflac-cli` build, and a Python environment with the SpotiFLAC
module. There is nothing else to install.

    docker run -d \
      -p 8484:8484 \
      -e SPF_API_KEY=$(openssl rand -hex 16) \
      -v /srv/downloads:/downloads \
      -v spotiflac-data:/data \
      ghcr.io/fishingpvalues/spotiflac-lidarr-proxy:latest

Put it on the same Docker network as Lidarr and give both the same downloads
volume, mounted at the same path, so Lidarr can import what this writes.
[`docker-compose.yml`](docker-compose.yml) is a working example.

Binaries for linux, macOS and Windows on amd64 and arm64 are attached to each
[release](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/releases),
along with `checksums.txt`. Running outside a container means supplying
`spotiflac-cli` yourself; it ships as a separate archive in the same release.

    SPF_API_KEY=... SPF_OUTPUT_DIR=/srv/downloads \
    SPF_SPOTIFLAC_CLI_PATH=/usr/local/bin/spotiflac-cli \
    ./spotiflac-lidarr-proxy serve

`-v` raises the log level to debug, `-vv` to trace.

## Configuring Lidarr

Two entries, both pointing at this program. Use the same key for each.

Settings, Download Clients, Add, SABnzbd:

    Host      spotiflac-proxy
    Port      8484
    API Key   your SPF_API_KEY
    Category  music

Settings, Indexers, Add, Newznab:

    URL         http://spotiflac-proxy:8484/api/newznab
    API Key     your SPF_API_KEY
    Categories  3010, 3040

Set the download client's category to `music`, or Lidarr's test reports "a
category is recommended".

The indexer answers directed searches only. Lidarr's RSS sync sends a query
with no artist and no album, and there is nothing to browse, so it returns
nothing by design. For the same reason Lidarr's indexer Test button reports
"Query successful, but no results in the configured categories were
returned". That is expected here and does not stop searches working; the
button has no way to express "search-only indexer".

## How downloads happen

Lidarr searches, picks a release, and hands the download to the SABnzbd side.
Each job then goes through up to four backends, in order, first success wins.

| # | Backend | Requires | Human interaction |
|---|---------|----------|-------------------|
| 1 | SpotiFLAC Python module | in the image | none |
| 2 | `spotiflac-cli` against a custom Tidal or Qobuz API | `SPF_TIDAL_API_URL` or a reachable fallback | none |
| 3 | `spotiflac-cli` with a captcha solver | `SPOTIFLAC_FSL_URL` | none |
| 4 | `spotiflac-cli` against the community tier | nothing | solve a captcha in a browser |

Backend 1 handles its own authentication and needs no browser, which is why it
is tried first. Backends 2 through 4 exist because upstream sources fail often
and independently.

Jobs are recorded in SQLite and survive a restart, including ones that had not
started yet. Completion is verified against the files on disk rather than
assumed from a successful request. A per-service circuit breaker stops sending
work to a service after five consecutive failures and reopens after ten
minutes; `SPF_FALLBACK_SERVICES` lets a job try another service instead of
failing.

### Public Tidal API mirrors

Backend 2 needs a Tidal API instance. `SPF_TIDAL_API_FALLBACK_URLS` ships a
list of known public ones, probed in order; the first that answers 2xx with a
JSON body is used, and that verdict is cached for five minutes. Instances that
are down cost one bounded probe rather than blocking the list behind them.

A note on what qualifies. `lossless.wtf`, `monochrome.samidy.com` and
`if-it-runs-ship-it.lol` are the Monochrome *web interface*, not its API. They
answer 200 with HTML. Do not configure them as API URLs.

State of the public list, measured 2026-08-07 from a VPN exit and from a bare
connection, with identical results:

| Instance | Result |
|----------|--------|
| `monochrome-api.samidy.com` | reachable, hifi-api 2.3; metadata works, `/track/` returns 403 |
| `api.monochrome.tf` | HTTP 503 |
| `arran.monochrome.tf` | HTTP 502 |
| `hifi-one.spotisaver.net` | HTTP 502 |
| `*.qqdl.site`, `tidal.kinoplus.online` | connection refused |
| `tidal.qqdl.site` | Cloudflare interstitial |
| `hifi.geeked.wtf` | NXDOMAIN |

That is not a configuration problem and no setting works around it. hifi-api's
own documentation notes that Tidal began blocking the accounts these instances
run on, so a reachable instance can still fail every track lookup. Public
mirrors come back; the list is probed fresh, so recovery needs no action.

Instances speaking the [hifi-api](https://github.com/uimaxbai/hifi-api) format
return a base64 manifest rather than a download URL. Those are detected from
their root response and a translating adapter is started in front of them
automatically. The adapter also waits out hifi-api's playback queue, which
answers 202 with a ticket when every upstream credential is busy.

Self-hosting your own instance and pointing `SPF_TIDAL_API_URL` at it avoids
all of the above, and is the only arrangement that does not depend on
someone else's uptime.

## Captcha solving

Backend 4 requires solving a Cloudflare Turnstile in a real browser, once.
Backend 3 avoids that by delegating to a headless solver over the FlareSolverr
API. Point `SPOTIFLAC_FSL_URL` at one.

[trawl](https://github.com/germondai/trawl) is the recommended solver.
FlareSolverr is effectively unmaintained against current Turnstile, and trawl
keeps the same API and port, so it is a drop-in replacement:

    services:
      trawl:
        image: ghcr.io/germondai/trawl:latest
        environment:
          - PORT=8191
          - BROWSER_POOL_SIZE=2

      proxy:
        image: ghcr.io/fishingpvalues/spotiflac-lidarr-proxy:latest
        environment:
          - SPOTIFLAC_FSL_URL=http://trawl:8191
          - SPOTIFLAC_ADDRESS=proxy

`SPOTIFLAC_ADDRESS` must be an address the solver's browser can reach back on,
since the challenge redirects there. It is auto-detected from the default
route, which is wrong often enough to be worth setting explicitly.

This path is best-effort. The challenge page is itself behind Cloudflare and
sometimes rejects headless browsers outright. Backends 1 and 2 need none of
this, and are the ones to prefer.

## Running behind a VPN

Every backend talks to third-party servers that will log the connecting
address. Route the container through a VPN sidecar. With
[gluetun](https://github.com/qdm12/gluetun), share its network namespace:

    services:
      proxy:
        network_mode: "service:gluetun"
        depends_on:
          gluetun:
            condition: service_healthy

Sharing a namespace has consequences worth stating plainly. Every container
doing this reaches the others on `127.0.0.1`, and `depends_on` alone does not
survive the VPN container restarting, which takes the network with it. Publish
ports on the gluetun service, not on this one.

`HTTP_PROXY`/`HTTPS_PROXY` are honoured for outbound requests if you would
rather use gluetun's HTTP proxy than its namespace. They are deliberately
stripped from the `spotiflac-cli` subprocess, which talks to public endpoints
directly and mishandles a proxy that speaks HTTP to an HTTPS client.

WireGuard sessions expire silently: the tunnel reports healthy and passes no
packets. If every backend starts failing at once, check egress before
suspecting this program.

## Configuration

All variables are prefixed `SPF_`, except the two SpotiFLAC ones. Full
reference in [`docs/API.md`](docs/API.md).

| Variable | Default | Meaning |
|----------|---------|---------|
| `SPF_API_KEY` | required | Shared secret for Lidarr |
| `SPF_PORT` | 8484 | Listen port |
| `SPF_OUTPUT_DIR` | /downloads | Where finished audio is written |
| `SPF_DB_PATH` | /data/queue.db | Job queue database |
| `SPF_DEFAULT_SERVICE` | tidal | tidal, qobuz, amazon or deezer |
| `SPF_DEFAULT_QUALITY` | lossless | lossless or hires |
| `SPF_FALLBACK_SERVICES` | none | Services tried after the primary fails |
| `SPF_MAX_CONCURRENT` | 3 | Concurrent downloads |
| `SPF_JOB_TIMEOUT` | 30m | Ceiling per job |
| `SPF_HISTORY_RETENTION_COUNT` | 500 | History rows kept |
| `SPF_LOG_LEVEL` | info | trace, debug, info, warn, error |
| `SPF_TIDAL_API_URL` | none | Custom Tidal API instance |
| `SPF_QOBUZ_API_URL` | none | Custom Qobuz API instance |
| `SPF_TIDAL_API_FALLBACK_URLS` | built-in list | Comma-separated, probed in order |
| `SPF_SPOTIFLAC_CLI_PATH` | /usr/local/bin/spotiflac-cli | CLI binary |
| `SPF_SPOTIFLAC_PYTHON_VENV` | /venv/bin/python3 | Python environment for backend 1 |
| `SPF_VERIFY_RELAY_URL` | none | This program's reachable `/verify/callback` URL |
| `SPF_VERIFY_NOTIFY_URL` | none | Webhook posted to when verification is needed |
| `SPF_VERIFY_NOTIFY_TITLE` | SpotiFLAC verification needed | `Title` header for that webhook |
| `SPOTIFLAC_FSL_URL` | none | FlareSolverr-compatible solver |
| `SPOTIFLAC_ADDRESS` | auto | Address the solver can reach this program on |

`SPF_FALLBACK_SERVICES` deliberately excludes `amazon` by default. That
provider drives a real browser, which this image does not ship, so it fails
after writing an undecryptable `.enc` file into the job directory.

Amazon categories map onto Lidarr quality profiles through the SABnzbd
category name: `music-tidal`, `music-qobuz-flac-24` and so on select service
and quality per job.

## Security

Authentication is a single static API key, the same model SABnzbd itself uses.
The key is compared in constant time and redacted from request logs. Values
reaching the `spotiflac-cli` subprocess are matched against an anchored
allowlist first, so a crafted release name cannot inject arguments, and job
directories are derived from server-generated identifiers rather than
user-supplied paths.

Three endpoints are deliberately unauthenticated:

- `/health` reports which internal checks failed.
- `/metrics` exposes Prometheus counters and queue depth.
- `/api/verify-relay` receives the captcha grant, which arrives as a browser
  redirect carrying no API key. Its forwarding target is taken from the
  state-to-callback mapping this program recorded when it dispatched the
  challenge, never from the request. An unrecognised state falls back to the
  supplied value only if it is plain http, on loopback, at `/session-grant`;
  redirects are not followed and the upstream response body is logged rather
  than returned.

Everything else requires the key, including `mode=version` and `t=caps` on any
route that is not actually invoking them.

Do not publish the port to the internet. Keep it on Lidarr's network, and
reach it remotely over Tailscale or WireGuard rather than a forwarded port. If
the key leaks, change it; there is no session state to invalidate.

`GET /api?mode=warnings&apikey=...` lists open circuit breakers and pending
verifications, and is worth checking when downloads stop.

## Why a proxy and not a Lidarr plugin

Lidarr plugins are .NET assemblies loaded into Lidarr's process, available
only on its separate plugins branch, and a fault in one has Lidarr's access. A
separate process speaking a protocol Lidarr has supported for years keeps
working across Lidarr releases, can be put behind whatever VPN sidecar you
already run, and cannot take Lidarr down with it.

## Troubleshooting

Repeated failures against one service are usually address-based rate limiting
rather than an authentication problem; `mode=warnings` will show the breaker
open. Set `SPF_FALLBACK_SERVICES` so jobs move on by themselves.

If every service fails at once, the cause is upstream or network, not
configuration. Confirm egress works, then check whether the Tidal mirrors are
answering at all.

For `browser integration is not ready` on the CLI backend, in order of
reliability: use backend 1, which is in the image and needs none of this;
point `SPF_TIDAL_API_URL` at an instance you control; run the
`:latest-gui` image alongside this one, sharing the app-data volume, and
complete verification once in the browser it exposes over noVNC (see
[`docker-compose.gui.yml`](docker-compose.gui.yml)); or set
`SPF_VERIFY_RELAY_URL` and open the link from `mode=warnings` yourself.

## API reference

Route tables and response fields are in [`docs/API.md`](docs/API.md). The
machine-readable specification is [`openapi.json`](openapi.json), which CI
checks against the running server on every build.

## Development

    go test ./... -count=1
    INTEGRATION=1 go test ./tests/integration/... -v

The integration suite starts a real docker-compose stack.

This project was written with AI assistance. Everything goes through the same
tests and review as anything else here.

## Legal

SpotiFLAC and this program are third-party tools with no affiliation to
Spotify, Tidal, Qobuz, Amazon Music, Deezer or any other service. You are
responsible for complying with the law where you live and with the terms of
the services involved. Provided as is, without warranty; the author accepts no
liability for bans, damages or legal consequences arising from its use.

Upstream API credits: [MusicBrainz](https://musicbrainz.org),
[LRCLIB](https://lrclib.net), [Songlink/Odesli](https://song.link),
[Songstats](https://songstats.com),
[hifi-api](https://github.com/binimum/hifi-api),
[Qobuz-DL](https://github.com/QobuzDL/Qobuz-DL).

## License

[Apache License 2.0](LICENSE).
