# Reliability, Security, Observability & Matching Hardening — Design Spec

**Date:** 2026-07-18
**Status:** Approved
**Repo:** `github.com/fishingpvalues/spotiflac-lidarr-proxy`

## Overview

Follow-up hardening pass on the existing spotiflac-lidarr-proxy (see `2026-07-18-spotiflac-lidarr-proxy-design.md` for original architecture). This spec was produced from a deep code-level gap analysis of the current implementation plus a competitive analysis of adjacent *arr-bridge projects (soularr, deemix/streamrip/OrpheusDL Lidarr integrations, debrid-service SABnzbd shims, and Prowlarr/Newznab impersonation conventions). It closes four independent gap classes found in that research, ordered by risk:

1. **Correctness + reliability** — silent false-success states, no dedup, no crash recovery, no retry/fallback.
2. **Security** — timing-unsafe auth check, secret leakage into logs, unvalidated CLI args, unpinned supply chain.
3. **Observability** — no metrics, a `/health` that can't actually detect an unhealthy proxy, no visibility into circuit-breaker state.
4. **Matching + onboarding docs** — release-matching signal Lidarr could use but isn't given, and a documentation gap around the real (rate-limit, not credential) failure mode of the underlying ripper.

No changes to the external Lidarr-facing contract (SABnzbd/Newznab route shapes) are made except where explicitly noted (new `warnings` content, new `newznab:attr` fields — both additive, non-breaking).

## Phase 1 — Correctness + Reliability

### 1.1 Completion verification

**Problem:** `processDownload` (`internal/api/sabnzbd/handler.go`) marks a job `Completed` as soon as the SpotiFLAC CLI process exits 0, without checking that a `type:"complete"` progress event was actually observed or that the number of files written matches the album's expected track count. A crash after the last track, or a CLI that exits 0 without emitting completion, silently reports success to Lidarr — which then imports a partial/empty album and (per Lidarr's own issue history, e.g. #4342/#2746) never re-attempts it.

**Design:**
- `spotiflac.Client.Download` already streams `ProgressEvent`s; the event parser (`progress.go`) must expose a terminal `Complete bool` / `Failed bool` marker per event stream, not just "channel closed."
- `processDownload` tracks whether a `complete` event was actually received. If the process exits 0 without one, treat as `Failed` with reason `"cli exited without completion signal"`.
- Before marking `Completed`, cross-check the number of audio files present in the job's output directory (`storage.CountAudioFiles(dir)`, new helper) against `MetadataResult.TrackCount` obtained at search time. Mismatch → `Failed` with reason `"partial album: N/M tracks"` rather than `Completed`. `TrackCount == 0` (unknown, e.g. single-track jobs) skips this check.
- These are the two independent signals (event stream + filesystem reality) — either one failing fails the job. This directly targets the exact failure class Lidarr itself has open issues about.

### 1.2 Duplicate job rejection

**Problem:** Re-adding the same Spotify URL (Lidarr commonly retries) creates a second unrelated `nzo_id` and a second concurrent download of the same content. No uniqueness constraint exists on `spotify_url` in the queue schema.

**Design:**
- Before creating a new job in `handleAddURL`, query the queue for an existing job with the same `spotify_url` whose status is non-terminal (`Queued` or `Downloading`).
- If found, return that job's existing `nzo_id` in the SABnzbd response instead of creating a new row — matching real SABnzbd/NZBGet dedup-by-content semantics that Lidarr already expects from a download client.
- No new DB column needed — an indexed lookup on `spotify_url` is enough given expected queue sizes; add an index in the `CREATE TABLE IF NOT EXISTS` migration.

### 1.3 Startup crash recovery

**Problem:** A job left in `Downloading` status when the process is killed (deploy, crash, OOM) stays `Downloading` forever — Lidarr sees a permanently "in progress" item that never completes or fails.

**Design:**
- On `queue.New()` startup, run a one-time sweep: any row with `status = 'Downloading'` is transitioned to `Failed` with `fail_message = "interrupted by restart"`.
- Deliberately **not** auto-resumed/re-launched — partial on-disk state from the killed subprocess is not trusted. Lidarr's own failed-download handling (retry/blocklist) takes it from there, consistent with how a real SABnzbd instance behaves after an unclean shutdown (queue entries default to a re-checkable state, not silently continued).

### 1.4 `change_cat` correctness

**Problem:** `handleChangeCat` updates `job.Category` but never re-derives `Service`/`Quality`, so changing category via Lidarr's UI has no actual effect on the next download attempt (it does for `addurl`, via the same category-parsing logic — this is an inconsistency, not a missing feature).

**Design:** Route `handleChangeCat`'s category value through the same category-parsing function `addurl.go` already uses (see 1.7 — this function is being consolidated anyway), updating `Service`/`Quality` alongside `Category` in the same DB write.

### 1.5 Retry + circuit breaker

**Problem:** No retry/backoff on a transient CLI failure; no circuit breaker for a fully-dead upstream service. A dead Tidal means every new job still spawns a fresh CLI process and occupies a concurrency slot for up to the full job timeout before failing — three such jobs stall the whole queue (`maxConcurrent` default 3).

**Design:**
- **Retry:** on job failure, retry the CLI invocation up to 2 times total with a short exponential backoff (e.g. 5s, 15s) before marking the job `Failed`. Retries happen within the same job/goroutine, not as new queue entries — Lidarr never sees the intermediate attempts.
- **Circuit breaker:** an in-memory, per-service counter of consecutive failures (process-lifetime state, no persistence needed). N consecutive failures (default 5) within a rolling window trips the breaker for that service for a cooldown period (default 10m); while tripped, new jobs targeting that service fail immediately with `"service <x> temporarily unavailable (circuit open)"` instead of spawning a CLI process and waiting out a timeout. Breaker state per service is exposed via the `warnings` endpoint (Phase 3).

### 1.6 Quality/service fallback

**Problem:** If the requested service/quality combination isn't available for a track, the job just fails — no automatic degrade.

**Design:** New config `SPF_FALLBACK_SERVICES` (comma list, e.g. `tidal,qobuz,amazon,deezer`; empty = disabled, the default). On a job failure where a fallback chain is configured, before exhausting retries the proxy tries the *next* service in the chain (same requested quality) once. This is a single fallback attempt per job, not a full cross-product retry matrix — keeps behavior predictable and bounded.

### 1.7 Cleanup of existing bugs

- Delete unused, buggy `ParseCategory` in `internal/config/config.go` (the `cat = cat // keep lowercase` no-op) — consolidate on the working, already-lowercased category parser currently private to `internal/api/sabnzbd/addurl.go`; promote it to `internal/config` as the single source of truth, used by both `addurl.go` and the `change_cat` fix in 1.4.
- Fix `internal/spotiflac/client.go`'s self-assignment bug: `artist := raw.Artist; if artist == "" { artist = raw.Artist }` → fall back to `raw.Name` when `Artist` is empty.
- `internal/indexer/newznab.go`'s hardcoded `size=0`/`Enclosure.Length=0`: replace with an estimate derived from `TrackCount * average-bytes-per-track` (constant per quality tier — e.g. ~35MB/track for 16-bit lossless, ~90MB/track for hi-res — clearly an estimate, not exact, but nonzero so Lidarr's size-based checks don't reject releases outright).

## Phase 2 — Security

### 2.1 Constant-time API key comparison
Replace `key != apiKey` in `internal/api/middleware.go` with `subtle.ConstantTimeCompare`. Remove the now-fully-dead `APIKeyAuth` function (only `APIKeyAuthWithSkiplist` is wired) rather than fixing both — one code path, one fix.

### 2.2 Stop leaking the API key into logs
`RequestLogger` currently logs the raw query string verbatim, including `apikey=...`. Redact the `apikey` query parameter value before logging (replace with `***`).

### 2.3 Validate Spotify URLs before they reach the CLI
`addurl`'s `name`/`nzbname` parameter is passed straight through as an unvalidated `--url` argument to `spotiflac-cli`. Add a strict regex check (`^https://open\.spotify\.com/(intl-[a-z-]+/)?(track|album|playlist)/[A-Za-z0-9]+`) before enqueueing; reject (SABnzbd-style error response) anything that doesn't match. This closes the CLI argument-injection vector, not just cosmetic input validation.

### 2.4 Supply-chain: pin the SpotiFLAC fork build
`Dockerfile`'s `cli-builder` stage does `git clone https://github.com/fishingpvalues/SpotiFLAC.git` with no ref pinned — every image build floats to whatever is on `main`. Pin to a specific commit SHA (checked in as a build arg / Dockerfile `ARG`, bumped deliberately, similar to how `renovate.json` already manages other deps). Also add a non-root `USER` in the final runtime stage.

### 2.5 Document TLS/reverse-proxy expectations
No in-proxy TLS support is being added (out of scope — this is meant to sit behind a reverse proxy in virtually all real deployments, same as Lidarr itself). README gets an explicit "run this behind a TLS-terminating reverse proxy; do not expose the plain-HTTP port directly to the internet" note plus a docker-compose snippet showing a Caddy/Traefik sidecar pattern.

## Phase 3 — Observability

### 3.1 `/metrics` (Prometheus)
Add `github.com/prometheus/client_golang` and expose `/metrics`: job counter by status × service (`spf_jobs_total{status,service}`), download duration histogram (`spf_download_duration_seconds{service,quality}`), current queue depth gauge (`spf_queue_depth{status}`).

### 3.2 Real `/health`
Current `/health` is a static `{"status":"ok"}`. Replace with checks that actually matter for this proxy: SQLite `Ping()`, `os.Stat` + executable-bit check on `SpotiflacCLIPath`, and free disk space on `OutputDir` above a low-water mark (reuse `storage.GetDiskSpace`). Any failing check → `503` with which check failed in the body. Keep response shape simple (this is consumed by Docker's healthcheck, not Lidarr).

### 3.3 `warnings` reflects real state
`handleWarnings` currently always returns an empty array. Surface: open circuit breakers (from 1.5) with time-until-retry, and jobs stuck in `Downloading` past 2× their expected timeout (a sign something's wrong even before the hard timeout fires).

### 3.4 Persist CLI output for postmortem
On job failure, store the last ~4KB of the CLI's combined stdout/stderr in a new `queue` table column (`cli_output TEXT`, nullable, only populated on failure to avoid bloating the DB on the happy path). Not surfaced in the SABnzbd protocol responses (Lidarr doesn't need it) — accessible only via direct DB inspection for operator debugging, which is the actual gap being closed (today, failure reasons are truncated to a single-line `fail_message`).

## Phase 4 — Matching + Onboarding Docs

### 4.1 Correct the credential-onboarding documentation gap
Initial research assumed missing docs on Tidal/Qobuz/Amazon/Deezer credentials. Verified against the upstream `fishingpvalues/SpotiFLAC` (fork of `spotbye/SpotiFLAC`) README: **no account or credentials are used at all** — it reverse-engineers the Spotify web player for metadata and pulls audio via third-party APIs. There is nothing to document here; this item is dropped.

The upstream README's own FAQ confirms the *actual* operational failure mode: IP-based rate limiting ("this usually happens because your IP address has been rate-limited... use a VPN to bypass"). README gets a troubleshooting entry explaining that repeated/rapid job failures for a given service likely indicate rate-limiting, and pointing at the Phase 1.5 circuit breaker / `warnings` endpoint (Phase 3.3) as the place to observe it — this is the real mitigation already designed above, just needs to be documented as such.

### 4.2 Surface ISRC to aid Lidarr matching
`MetadataResult.ISRC` is already parsed from the CLI's search output but never used. Add it as a `newznab:attr name="isrc"` on search results (additive to the existing XML) so Lidarr/Prowlarr-side tooling that considers ISRC has the signal available. No MusicBrainz ID is fabricated — the CLI doesn't provide one, and inventing one would actively harm Lidarr's matching, so this stays ISRC-only.

### 4.3 Enforce history retention
`Misc.HistoryRetentionNumber` is parsed into config but never enforced — history rows accumulate forever. Add a periodic prune (on each `addurl` call, cheap opportunistic sweep — no new background goroutine needed) deleting `is_history=1` rows beyond the configured count, oldest first.

## Testing

- Phase 1: unit tests for completion-verification (event-seen-but-file-count-mismatch, exit-0-without-complete-event), dedup rejection, startup-recovery sweep, circuit breaker state transitions (closed→open→half-open), fallback chain exhaustion.
- Phase 2: test that a non-Spotify-URL `addurl` is rejected; test that logged output never contains the literal API key value.
- Phase 3: `/health` returns 503 when DB/CLI-path checks are forced to fail (test doubles); `/metrics` exposes expected metric names.
- Phase 4: newznab XML contains `isrc` attr when present in search results; history prune leaves exactly the configured retention count.

All new code follows existing conventions (table-driven tests, `testify`, no global state, errors wrapped with `%w`, zerolog structured logging) per `AGENTS.md`.
