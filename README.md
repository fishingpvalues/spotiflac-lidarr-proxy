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
matching `spotiflac-cli` build, a Python environment with the SpotiFLAC
module, and the browser stack that module drives - Chromium, Node.js and
Xvfb. There is nothing else to install and no browser to supply.

Chromium is baked in on purpose. SpotiFLAC's providers are Node extensions
and its Turnstile solver drives a real (non-headless) Chromium under Xvfb, so
an image without them cannot run backend 1 at all - which is the backend this
proxy tries first and the only one that needs no captcha and no third-party
API mirror. Earlier images shipped the Python module without the browser, so
backend 1 failed on every job and the failure looked like a network problem.

    docker run -d \
      -p 8484:8484 \
      --shm-size=512m \
      -e SPF_API_KEY=$(openssl rand -hex 16) \
      -v /srv/downloads:/downloads \
      -v spotiflac-data:/data \
      ghcr.io/fishingpvalues/spotiflac-lidarr-proxy:latest

`--shm-size` guards the Chromium the Python backend drives. At Docker's
default 64 MB of `/dev/shm` a headless Chromium prints GPU init errors and
then no page at all; the same command with `--disable-dev-shm-usage` works.
SpotiFLAC's solver does pass that flag, so this is insurance rather than a
requirement - but it costs nothing and a launch path that forgets the flag
hangs silently.

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

### Running as a different user

The image runs as uid/gid **1000**, and `$HOME` (`/home/spotiflac`) belongs to
that id. Override it at build time if your host needs another:

    docker build --build-arg PUID=1001 --build-arg PGID=1001 .

Do not override it at *run* time with `--user` or compose's `user:` unless
that id owns `$HOME`. Chromium cannot create its crashpad database under an
unwritable home directory and dies with SIGTRAP, which surfaces as
`Browser failed to start within timeout`, then 120-second extension timeouts,
then every download failing with `exit status 1` - a failure mode that looks
like a network problem and is not. See Troubleshooting.

## Configuring Lidarr

Two entries, both pointing at this program, both using the same `SPF_API_KEY`.
Lidarr has to reach the host and port you publish; if the proxy shares a VPN
sidecar's network namespace, that is the sidecar's name and published port,
not this container's.

### 1. The download client

Settings, Download Clients, Add, **SABnzbd**:

| Field | Value |
|-------|-------|
| Name | SpotiFLAC |
| Host | `spotiflac-proxy` |
| Port | `8484` |
| URL Base | leave empty |
| API Key | your `SPF_API_KEY` |
| Username / Password | leave empty |
| Category | `music` |
| Use SSL | off |
| Client Priority | above your usenet and torrent clients only if you want FLAC preferred |

Press **Test**. It must go green. Category is not optional - without it Lidarr
warns that one is recommended, and every job lands in the default.

The category is also the download's routing: `music-flac-24` asks for hi-res,
`music-tidal` pins the provider, `music-qobuz-flac-16` does both. `mode=get_cats`
lists them all. Plain `music` uses `SPF_DEFAULT_SERVICE` and
`SPF_DEFAULT_QUALITY`.

Categories carry a name but deliberately no directory. Downloads land in
`SPF_OUTPUT_DIR/<nzo_id>`, never in a per-category subdirectory, and Lidarr
resolves a relative category dir against `complete_dir` and then checks the
result exists inside its own container - so advertising one raises a permanent
"places downloads in ... but this directory does not appear to exist" health
error against a working setup.

### 2. The indexer

Settings, Indexers, Add, **Newznab** (Custom):

| Field | Value |
|-------|-------|
| Name | SpotiFLAC |
| URL | `http://spotiflac-proxy:8484` |
| API Path | `/api/newznab` |
| API Key | your `SPF_API_KEY` |
| Categories | `3000`, `3010`, `3040` |
| Additional Parameters | leave empty |
| Enable RSS Sync | see below |
| Enable Automatic Search | on |
| Enable Interactive Search | on |

Categories must include the ones this indexer publishes or Lidarr filters
every result away. `t=caps` declares the full set; releases are tagged `3000`
plus `3010` for lossless, or `3000` plus `3040` for hi-res.

### 3. Make the indexer Test pass

Out of the box the Test button reports:

    Query successful, but no results in the configured categories were
    returned from your indexer.

That is Lidarr's reaction to an empty browse feed, not a fault. This indexer
resolves Spotify metadata for a named album; it has no notion of "what is
new", so `t=music` with no artist and no album has nothing to answer with.
Directed searches - which is all Lidarr does when it actually looks for an
album - work regardless, and so does grabbing.

To make the button go green, give the browse feed a search to run:

    SPF_RSS_QUERY=new albums

Anything Spotify's search understands works. The results are ordinary album
releases, so Lidarr ignores every one that does not match an album it is
monitoring. Leave it unset to keep RSS sync silent and accept the red Test.

### 4. Priorities

Delay profiles decide which client wins when several can serve a release.
This proxy speaks the usenet protocol, so it competes with your usenet
clients. A reasonable arrangement:

- real usenet client: priority 1
- SpotiFLAC: priority 5
- torrent client: last

Lidarr treats a lower number as higher priority.

### 5. Verify end to end

Pick a monitored album, run an interactive search, and grab a SpotiFLAC
release. Then check all three views agree:

    # the proxy's own queue
    curl -s "http://localhost:8484/api?mode=queue&output=json&apikey=$SPF_API_KEY"

    # Lidarr's queue - the row must be here, with a title and a size
    curl -s -H "X-Api-Key: $LIDARR_KEY" "http://localhost:8686/api/v1/queue"

    # and afterwards
    curl -s "http://localhost:8484/api?mode=history&output=json&apikey=$SPF_API_KEY"

A queue slot with an empty `filename` means Lidarr cannot track the download
and will never import it. That was a real defect, fixed - if you see it,
the image predates the fix.

### Albums, not tracks

A Spotify search returns both tracks and the albums containing them, and both
rendered here as the same release title. Only the album is importable: grabbing a
track URL downloads exactly one file, and Lidarr refuses to import an album match
below 80% ("Album match is not close enough: 76.5 % vs 80 %", "Has missing
tracks"). So an album hit now wins its `(primary artist, album)` pair and the
track hits under it are dropped. Tracks with no album hit above them - singles -
are still published, and a single is a complete release in Lidarr's eyes.

Grabbing an album URL makes SpotiFLAC download every track into one job
directory, which is what Lidarr's importer expects to find.

## How downloads happen

Lidarr searches, picks a release, and hands the download to the SABnzbd side.
Each job then goes through up to four backends, in order, first success wins.

| # | Backend | Requires | Human interaction |
|---|---------|----------|-------------------|
| 1 | SpotiFLAC Python module + bundled Chromium | in the image, plus a writable `$HOME` | none |
| 2 | `spotiflac-cli` against a custom Tidal or Qobuz API | `SPF_TIDAL_API_URL` or a reachable fallback | none |
| 3 | `spotiflac-cli` with a captcha solver | `SPOTIFLAC_FSL_URL` | none |
| 4 | `spotiflac-cli` against the community tier | nothing | solve a captcha in a browser |

Backend 1 handles its own authentication, which is why it is tried first. Its
providers are Node extensions and some of them drive Chromium under Xvfb, all
three of which are in the image. The only thing it needs from the host is a
writable `$HOME` for the browser's profile and crash-handler state - see
Troubleshooting if downloads fail with a bare `exit status 1`. Backends 2 through 4 exist because upstream sources fail often
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

Backend 1 solves Turnstile itself with the bundled Chromium, so in the normal
case none of this section applies. It is the fallback backends that need an
external solver.

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
| `SPF_PYTHON_BUDGET` | 20m | Ceiling for the Python-backend phase of one attempt (clamped to `SPF_JOB_TIMEOUT`) |
| `SPF_SKIP_PYTHON_WHEN_SESSION_PRESENT` | true | Skip the Python phase when a valid CLI community session exists (CLI-implemented services only) |
| `SPF_HISTORY_RETENTION_COUNT` | 500 | History rows kept |
| `SPF_RSS_QUERY` | none | Search answering Lidarr's browse feed; also what makes its indexer Test pass |
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

**Downloads fail after ~2 minutes and the log mentions
`Command timeout: NetworkMethod.ENABLE`.** The browser started but its first
CDP command never answered. Check `$HOME` is writable (below) first, then CPU
headroom: two providers each launch their own browser, and a container capped
at 2 CPUs alongside Xvfb can starve them. Raising `--shm-size` is worth
trying but is not usually the cause - SpotiFLAC's solver already passes
`--disable-dev-shm-usage`.

**Every download fails with `exit status 1` and the log mentions
`Browser failed to start within timeout`.** The user the container runs as
cannot write `$HOME`. Check it directly:

    docker exec <container> sh -c 'id; ls -ld "$HOME"; touch "$HOME/.probe"'

A `Permission denied` there is the whole bug: Chromium aborts with
`chrome_crashpad_handler: --database is required` followed by
`Trace/breakpoint trap (core dumped)`, pydoll reports a start timeout, the
node extensions time out after 120 s, and backend 1 fails for every job. Run
the image as the uid it was built for, or rebuild with matching `PUID`/`PGID`.

**Search results show 0 tracks, 0 B and no year, and Lidarr rejects them with
"Album match is not close enough ... [year, country, tracks]".** The Python
backend is not answering searches, so the numbers come from `spotiflac-cli`,
which reports none of them. Same check as above.

Repeated failures against one service are usually address-based rate limiting
rather than an authentication problem; `mode=warnings` will show the breaker
open. Set `SPF_FALLBACK_SERVICES` so jobs move on by themselves.

If every service fails at once, the cause is upstream or network, not
configuration. Confirm egress works, then check whether the Tidal mirrors are
answering at all. A failed job now carries the backend's own reasons, so read
those before changing settings. The ones seen in practice, and what they mean:

| Reason | Cause |
|--------|-------|
| `ext:tidal-web: NETWORK_ERROR: Timeout (120s) calling download` | the extension's upstream API is not answering |
| `ext:qobuz-web: NETWORK_ERROR: Timeout (120s) calling checkAvailability` | same |
| `ext:amazon: Track not available: not_found_on_amazon` | that provider genuinely does not have it |
| `ext:deezer: <asyncio.locks.Lock ...> is bound to a different event loop` | an upstream SpotiFLAC bug: `core/session_memory.py` and `core/profiles.py` hold module-level `asyncio.Lock()` objects, which bind to whichever loop first awaits them and then reject every other one. Nothing configurable fixes it; treat Deezer as unreliable |

None of these are worked around here on purpose. Monkeypatching an installed
site-packages module is lost on every version bump and hides the problem in
the meantime.

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
