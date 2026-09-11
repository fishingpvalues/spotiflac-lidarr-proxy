# Operations

The detail behind [README.md](../README.md): the failure modes this proxy has
actually hit, the measurements behind how it responds to them, and the two
paths that need a human once.

Read the README first - nothing here is needed for a working install. This is
for the day something misbehaves.

## Running as a different user

The image runs as uid/gid **1000**, and `$HOME` (`/home/spotiflac`) belongs to
that id. Override it at build time if your host needs another:

    docker build --build-arg PUID=1001 --build-arg PGID=1001 .

Do not override it at *run* time with `--user` or compose's `user:` unless
that id owns `$HOME`. Chromium cannot create its crashpad database under an
unwritable home directory and dies with SIGTRAP, which surfaces as
`Browser failed to start within timeout`, then 120-second extension timeouts,
then every download failing with `exit status 1` - a failure mode that looks
like a network problem and is not. See Troubleshooting.

## Verify end to end

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

## Backpressure: when upstream says wait

Two upstream answers mean "stop asking", not "this release is broken":

- a scheduled break - `The server is taking a scheduled short break. Please
  try again in about 104 minute(s).` - which the shared community
  infrastructure returns to every caller under load, and
- a rate limit - `Tidal community API rate limited (429)`.

Both park the whole queue instead of being retried. The park lasts the
announced duration for a break, and `SPF_RATE_LIMIT_PARK_S` (default 90s) for
a 429, and it is taken BEFORE the concurrency semaphore, so parked jobs hold
no slot. `mode=warnings` reports the remaining time.

The job that ran into the answer is put **back into the queue**, not failed.
Failing it would be a lie Lidarr cannot recover from on its own: the release
was never tried against a working API, and once the item is in Lidarr's
history only a fresh search brings it back. A job may be requeued three times
this way; after that it fails for real, so a permanently broken release cannot
masquerade as a download that is forever about to start.

Measured, without this: a 47-job burst triggered a 104-minute break and the
whole backlog failed into history; and later, with a single concurrency slot,
one job spent ~25 minutes retrying into 429s while six more queued behind it.

## Public Tidal API mirrors

Backend 2 needs a hifi-api instance. `SPF_TIDAL_API_FALLBACK_URLS` ships a
list of public ones, probed in order. The probe is two-stage on purpose: the
root must answer JSON carrying both `version` and `Repo`, and then a sentinel
`/track/` call must not fail with anything other than 404. Root alone is not
enough - `monochrome-api.samidy.com` once answered `200 {"version":"2.3"}`
while every track call failed with `Token refresh failed: 403 from
auth.tidal.com`, and handing such an instance to `spotiflac-cli` turns every
track into a hard failure.

`lossless.wtf`, `monochrome.samidy.com` and `if-it-runs-ship-it.lol` are the
Monochrome *web interface*, not its API. They answer 200 with HTML. Do not
configure them as API URLs.

### State of the public list

Measured 2026-09-11 against the full published instance list (the
`spotbye/SpotiFLAC-Next` wiki page "Available HiFi API Instances"), from the
AirVPN exit **and** from a bare home uplink, with identical results - so this
is the mirrors being gone, not the VPN being blocked:

| Instance | Result |
|----------|--------|
| `monochrome-api.samidy.com` | **alive** - `200 {"version":"2.3","Repo":"...hifi-api"}` |
| `api.monochrome.tf` | HTTP 503 |
| `arran.monochrome.tf` | Cloudflare 1033 (origin tunnel down) |
| `wolf` / `maus` / `vogel` / `katze` / `hund`.qqdl.site | DNS resolves, TLS handshake refused |
| `triton.squid.wtf` | NXDOMAIN |
| `hifi-one.spotisaver.net`, `hifi-two.spotisaver.net` | NXDOMAIN |
| `tidal.kinoplus.online` | NXDOMAIN |
| `tidal-api.binimum.org` | NXDOMAIN |

One of ten alive. NXDOMAIN was confirmed against Quad9 and Cloudflare
resolvers independently, so it is not one resolver filtering.

Those five are **not** in the shipped list: a domain that does not resolve can
never be probed successfully and only costs a resolver timeout. Entries that
merely 503 or refuse TLS are kept - the host still exists, and a mirror coming
back costs one probe. squid.wtf itself is deprecated upstream.

### This is structural, not a bad week

Public instances breach Tidal's terms and get taken down, and Tidal has been
blocking the accounts behind them in bulk - which is why a reachable instance
can still fail every track lookup. No setting works around it.

The durable fix is to run your own and point `SPF_TIDAL_API_URL` at it.
[binimum/hifi-api](https://github.com/binimum/hifi-api) (a maintained fork of
`sachinsenal0x64/hifi`) ships a Dockerfile and a compose file; it needs one
Tidal account's OAuth refresh token, obtained by running its
`tidal_auth/tidal_auth.py` once, supplied either as `token.json` or as
`CATALOG_CLIENT_ID` / `CATALOG_CLIENT_SECRET` / `CATALOG_REFRESH_TOKEN`. Its
maintainers explicitly do not support exposing it on the open internet - keep
it on the same private network as this proxy.

Backend 1 needs none of this and is the one to prefer.

### hifi-api instances need an adapter

Instances speaking the [hifi-api](https://github.com/uimaxbai/hifi-api) format
return a base64 manifest rather than a download URL. Those are detected from
their root response and a translating adapter is started in front of them
automatically. The adapter also waits out hifi-api's playback queue, which
answers 202 with a ticket when every upstream credential is busy.

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

## Session renewal

Backend 4's session expires. When it does, every job that needs the community
tier fails its verification step until something mints a new one, so the
renewal is worth automating rather than watching for.

Set `SPF_SESSION_RENEW_CMD` and the server runs it whenever the community
session store holds nothing valid, before the job takes its concurrency slot:

    SPF_SESSION_RENEW_CMD=python3 /opt/solver/turnstile-llm-solve.py 10
    TURNSTILE_LLM_API_KEY=<key for a vision model>

`/opt/solver/turnstile-llm-solve.py` ships in the image. It drives the
bundled Chromium under Xvfb through a freshly minted challenge, uses a vision
model as a state judge (screenshot plus the live iframe text: click, wait or
done) and the accessibility tree for aim, then exchanges the resulting grant
and writes both session stores - the one the Go CLI reads and the one the
Python extensions read. It is stdlib-only, because this image's `python3` has
no third-party packages.

Renewal is serialized and rate-limited: one run at a time (two browsers at the
same challenge interfere), and never more often than
`SPF_SESSION_RENEW_MIN_INTERVAL_S` (default one hour), bounded by
`SPF_SESSION_RENEW_TIMEOUT_S` (default ten minutes). The outcome is logged
with whether a valid session actually appeared afterwards.

It is off unless you set the variable, deliberately. Cloudflare's decision is
made largely on the reputation of the address the browser connects from, and
from a datacenter or VPN egress a solve attempt can fail every time no matter
how correct the clicking is - in which case each attempt costs wall-clock time
and model tokens for nothing. Any other command works here too; the contract
is just "write a valid session store, exit".

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
