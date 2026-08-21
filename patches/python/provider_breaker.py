#!/usr/bin/env python3
"""Give SpotiFLAC's downloader fast cross-service failover.

The stock cascade tries every provider for every track, in order, sharing
one per-track budget (timeout_s) across the whole provider list. Measured
against a live outage (Tidal API down, qobuz/deezer Turnstile-gated):

  * a dead first service held each track for most of the 900s budget, so
    the services behind it got seconds to fail in - the "next backend"
    never actually got a chance;
  * the same dead service was then re-tried with a fresh full budget on
    EVERY subsequent track, multiplying the outage cost by the track count;
  * a 2-track album therefore burned the proxy's entire 30m job budget and
    the Go-side fallback chain started into an already-dead context.

Two changes fix both:

  1. Per-provider budget cap (_PROVIDER_BUDGET_S, default 300s): one
     provider may no longer consume more than this share of a track's
     budget, so a dead provider hands the rest of the track to the next
     service instead of starving it.

  2. Cross-track provider breaker (_PROVIDER_BREAKER_FAILURES, default 1):
     a provider that failed N tracks is skipped for the rest of the download.
     Within one album a provider that just failed will almost certainly fail
     again on the next track (same API outage, same blocked IP), and
     track_max_retries (default 0) already covers intra-track retries, so
     skipping after one inter-track failure loses no retry semantics while
     collapsing the all-dead worst case from N_tracks x sum(budgets) to
     roughly one pass over the provider list.

Both knobs are environment-tunable (SPOTIFLAC_PROVIDER_BUDGET_S,
SPOTIFLAC_PROVIDER_BREAKER_FAILURES) and degrade toward the stock behaviour
when set to 0 / a large value.

Applied at image build against a pinned version; exits non-zero when a
pattern is missing so a future pin bump fails the build loudly instead of
silently patching nothing.
"""

import pathlib
import sys


def replace_once(path: pathlib.Path, old: str, new: str, label: str) -> bool:
    """Replace old with new exactly once; idempotent via a marker comment.

    The marker is checked BEFORE the pattern: some replacements keep the
    original text as a prefix of the new one, so the old pattern would
    still match after patching and get applied twice without this order.
    """
    text = path.read_text(encoding="utf-8")
    marker = f"# PATCHED_MARKER_{label}"
    if marker in text:
        print(f"already patched: {path.name} ({label})")
        return True
    if old not in text:
        print(f"PATTERN NOT FOUND in {path} ({label}):\n{old}", file=sys.stderr)
        return False
    path.write_text(text.replace(old, new, 1), encoding="utf-8")
    print(f"patched: {path.name} ({label})")
    return True


# ---------------------------------------------------------------------------
# Patterns
# ---------------------------------------------------------------------------

SIG_OLD = '''async def download_one_async(
    metadata: TrackMetadata,
    output_dir: str,
    providers: list[BaseProvider],
    opts: DownloadOptions,
    position: int = 1,
    is_album: bool = False,
) -> DownloadResult:
    """Attempts to download a single track across all providers in order,
    with per-track retry if track_max_retries > 0.
    """
    stop_event = asyncio.Event()
    DownloadManager()
    errors: dict[str, str] = {}
    started_at = time.monotonic()
'''

SIG_NEW = '''# Patched by spotiflac-lidarr-proxy: fast cross-service failover.
# PATCHED_MARKER_signature
#
# _PROVIDER_BUDGET_S caps how long ONE provider may take for ONE track.
# Without it the whole per-track timeout_s budget is shared across the
# provider list, so a dead first service holds a track for its full budget
# and leaves the services behind it seconds to fail in.
#
# _PROVIDER_BREAKER_FAILURES skips a provider for the REST of the download
# once it has failed that many tracks. Within one album a provider that just
# failed will almost certainly fail again on the next track (same API
# outage, same blocked IP); re-trying it per track multiplied the outage
# cost by the track count. track_max_retries (default 0) already covers
# intra-track retries, so skipping after one inter-track failure loses no
# retry semantics.
try:
    _PROVIDER_BUDGET_S = int(os.environ.get("SPOTIFLAC_PROVIDER_BUDGET_S", "300"))
except ValueError:
    _PROVIDER_BUDGET_S = 300
try:
    _PROVIDER_BREAKER_FAILURES = int(os.environ.get("SPOTIFLAC_PROVIDER_BREAKER_FAILURES", "1"))
except ValueError:
    _PROVIDER_BREAKER_FAILURES = 1


async def download_one_async(
    metadata: TrackMetadata,
    output_dir: str,
    providers: list[BaseProvider],
    opts: DownloadOptions,
    position: int = 1,
    is_album: bool = False,
    provider_failures: dict | None = None,
) -> DownloadResult:
    """Attempts to download a single track across all providers in order,
    with per-track retry if track_max_retries > 0.

    provider_failures (patched by spotiflac-lidarr-proxy) is a dict shared
    across all tracks of this download, mapping provider name -> number of
    tracks it has failed; providers at or above the breaker threshold are
    skipped instead of being given another full budget.
    """
    stop_event = asyncio.Event()
    DownloadManager()
    errors: dict[str, str] = {}
    started_at = time.monotonic()
'''

LOOP_OLD = '''        for idx, provider in enumerate(providers):
            if idx > 0:
                is_ext = provider.name.startswith("ext:")'''

LOOP_NEW = '''        for idx, provider in enumerate(providers):
            # PATCHED_MARKER_skip
            # Patched by spotiflac-lidarr-proxy: a provider that already
            # failed earlier tracks is not given another full budget.
            if (
                provider_failures is not None
                and provider_failures.get(provider.name, 0) >= _PROVIDER_BREAKER_FAILURES
            ):
                safe_tqdm_write(
                    f"[#{position}] Skipping {provider.name}: failed on earlier track(s)"
                )
                continue
            if idx > 0:
                is_ext = provider.name.startswith("ext:")'''

TIMEOUT_OLD = '''                # Wrap inside asyncio.wait_for to enforce track timeout strictly at the IO level
                time_elapsed = time.monotonic() - started_at
                timeout_left = (
                    max(1, opts.timeout_s - time_elapsed) if opts.timeout_s else None
                )'''

TIMEOUT_NEW = '''                # Wrap inside asyncio.wait_for to enforce track timeout strictly at the IO level
                time_elapsed = time.monotonic() - started_at
                timeout_left = (
                    max(1, opts.timeout_s - time_elapsed) if opts.timeout_s else None
                )
                # PATCHED_MARKER_budget
                # Patched by spotiflac-lidarr-proxy: cap this provider's share
                # of the track budget so a dead provider cannot starve the
                # ones behind it.
                if _PROVIDER_BUDGET_S > 0:
                    if timeout_left is None:
                        timeout_left = float(_PROVIDER_BUDGET_S)
                    else:
                        timeout_left = min(timeout_left, float(_PROVIDER_BUDGET_S))'''

TIMEOUTEXC_OLD = '''            except asyncio.TimeoutError:
                stop_event.set()
                logger.warning(
                    "[downloader] timeout exceeded for track '%s'",
                    metadata.title,
                )
                safe_tqdm_write(
                    f"\\n  \u23f1  Timeout reached for '{metadata.title}' \u2014 skipping track.",
                )
                return DownloadResult.fail(
                    "none",
                    f"Download timed out after {opts.timeout_s}s",
                )'''

TIMEOUTEXC_NEW = '''            except asyncio.TimeoutError:
                # PATCHED_MARKER_timeout_split
                # Patched by spotiflac-lidarr-proxy: the per-provider budget
                # cap above can fire while the track budget still has room
                # left. Stock code treats every TimeoutError as "track over",
                # which would turn the cap into a fast way to lose the whole
                # track. Only fail the track when the TRACK budget is
                # actually exhausted; otherwise record the provider failure
                # and hand the rest of the track to the next provider.
                if opts.timeout_s and time.monotonic() - started_at >= opts.timeout_s:
                    stop_event.set()
                    logger.warning(
                        "[downloader] timeout exceeded for track '%s'",
                        metadata.title,
                    )
                    safe_tqdm_write(
                        f"\\n  \u23f1  Timeout reached for '{metadata.title}' \u2014 skipping track.",
                    )
                    return DownloadResult.fail(
                        "none",
                        f"Download timed out after {opts.timeout_s}s",
                    )
                errors[provider.name] = (
                    f"provider budget exceeded ({_PROVIDER_BUDGET_S}s)"
                )
                if provider_failures is not None:
                    provider_failures[provider.name] = (
                        provider_failures.get(provider.name, 0) + 1
                    )
                safe_tqdm_write(
                    f"  \u2717  [#{position}] {provider.name}  \u00b7  provider budget exceeded ({_PROVIDER_BUDGET_S}s)",
                    file=sys.stderr,
                )
                continue'''

RECORD_OLD = '''            errors[provider.name] = result.error or "unknown error"
            safe_tqdm_write('''

RECORD_NEW = '''            errors[provider.name] = result.error or "unknown error"
            # PATCHED_MARKER_record
            # Patched by spotiflac-lidarr-proxy: feed the cross-track breaker.
            if provider_failures is not None:
                provider_failures[provider.name] = (
                    provider_failures.get(provider.name, 0) + 1
                )
            safe_tqdm_write('''

WORKER_OLD = '''        max_concurrent = max(1, getattr(self._opts, "max_concurrent_downloads", 2))
        semaphore = asyncio.Semaphore(max_concurrent)'''

WORKER_NEW = '''        max_concurrent = max(1, getattr(self._opts, "max_concurrent_downloads", 2))
        semaphore = asyncio.Semaphore(max_concurrent)
        # PATCHED_MARKER_shared
        # Patched by spotiflac-lidarr-proxy: shared across all workers so a
        # provider that fails on one track is skipped on every other track
        # of this download (fast failover to the surviving services).
        provider_failures: dict[str, int] = {}'''

CALL_OLD = '''                    result = await download_one_async(
                        track,
                        out_dir,
                        self._providers,
                        self._opts,
                        position,
                        self._is_album,
                    )'''

CALL_NEW = '''                    # PATCHED_MARKER_call
                    result = await download_one_async(
                        track,
                        out_dir,
                        self._providers,
                        self._opts,
                        position,
                        self._is_album,
                        provider_failures,
                    )'''


def main(root: pathlib.Path) -> int:
    dl = root / "downloader.py"
    ok = True
    ok &= replace_once(dl, SIG_OLD, SIG_NEW, "signature")
    ok &= replace_once(dl, LOOP_OLD, LOOP_NEW, "skip")
    ok &= replace_once(dl, TIMEOUT_OLD, TIMEOUT_NEW, "budget")
    ok &= replace_once(dl, TIMEOUTEXC_OLD, TIMEOUTEXC_NEW, "timeout_split")
    ok &= replace_once(dl, RECORD_OLD, RECORD_NEW, "record")
    ok &= replace_once(dl, WORKER_OLD, WORKER_NEW, "shared")
    ok &= replace_once(dl, CALL_OLD, CALL_NEW, "call")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main(pathlib.Path(sys.argv[1])))
