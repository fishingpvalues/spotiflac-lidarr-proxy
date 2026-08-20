#!/usr/bin/env python3
"""SpotiFLAC Python module wrapper - JSON progress output for the Go proxy.

Two modes:

  --url <spotify url>   download, streaming JSON-line events
  --search <query>      resolve Spotify metadata, one `search_result` per line

Both talk the same JSON-lines protocol the Go side parses (see
internal/spotiflac/progress.go). SpotiFLAC's own console noise on stdout is
harmless: parseProgress skips every line that is not JSON.

Why the search mode lives here and not only in the CLI: `spotiflac-cli
--search` reports `track_count: 0` and `year: ""` for every hit, and the
Newznab indexer derives the release size, the `files` attr and the release
year from exactly those. With zeros, Lidarr sees a 0-byte release with no
year and scores it below its album-match threshold - "Album match is not
close enough: 77.6 % vs 80 % [year, country, tracks]" on every grab. The
bundled SpotiFLAC Python module has the real numbers (SpotifyMetadataClient),
so this asks it directly.
"""

import argparse
import asyncio
import io
import json
import logging
import os
import sys
import threading

AUDIO_EXTENSIONS = (".flac", ".mp3", ".m4a", ".alac", ".wav", ".ogg", ".opus")

# Canonical SpotiFLAC quality names. The Go side hands us its own vocabulary
# ("lossless", "hires"), and SpotiFLAC.core.quality.normalize_quality falls
# back to LOSSLESS for anything it does not recognize - which silently
# downgrades every hi-res request.
QUALITY_ALIASES = {
    "lossless": "LOSSLESS",
    "flac-16": "LOSSLESS",
    "cd": "LOSSLESS",
    "16": "LOSSLESS",
    "hires": "HI_RES_LOSSLESS",
    "hires-lossless": "HI_RES_LOSSLESS",
    "hires_lossless": "HI_RES_LOSSLESS",
    "hi_res_lossless": "HI_RES_LOSSLESS",
    "flac-24": "HI_RES_LOSSLESS",
    "24": "HI_RES_LOSSLESS",
    "both": "HI_RES_LOSSLESS",
}


def emit(event_type, **kwargs):
    kwargs["type"] = event_type
    print(json.dumps(kwargs), flush=True)


def normalize_quality(quality):
    if not quality:
        return "LOSSLESS"
    return QUALITY_ALIASES.get(quality.strip().lower(), quality.strip().upper())


def release_year(release_date):
    """Year out of an ISO-ish release date, 0 when there isn't one.

    Spotify hands back "2013-05-17", "2013" or "". Lidarr wants a year and
    treats a literal 0 as a real year that fails to match, so callers must
    omit the attribute rather than publish a zero.
    """
    if not release_date:
        return 0
    head = str(release_date).strip()[:4]
    return int(head) if head.isdigit() else 0


def audio_files(directory):
    found = []
    for root, _dirs, files in os.walk(directory):
        for f in files:
            if f.lower().endswith(AUDIO_EXTENSIONS):
                found.append(os.path.join(root, f))
    return sorted(found)


def common_directory(paths, fallback):
    """Deepest directory containing every downloaded file.

    This is what gets reported as the job's output path, and Lidarr imports
    from it. Reporting a single file instead - which is what
    `path=downloaded[0]` used to do - makes Lidarr import exactly one track
    of an album and then report the release as "Has missing tracks" forever.
    """
    if not paths:
        return fallback
    directories = [os.path.dirname(p) for p in paths]
    common = os.path.commonpath(directories) if len(directories) > 1 else directories[0]
    return common or fallback


def total_size(paths):
    size = 0
    for p in paths:
        try:
            size += os.path.getsize(p)
        except OSError:
            continue
    return size


def parse_filename_info(filepath):
    """Extract artist/title from a filename like 'Title - Artist.flac'."""
    basename = os.path.splitext(os.path.basename(filepath))[0]
    parts = basename.split(" - ", 1)
    if len(parts) == 2:
        return parts[1].strip(), parts[0].strip()
    return "", basename


def quiet_loggers():
    logging.getLogger().setLevel(logging.WARNING)
    logging.getLogger("SpotiFLAC").setLevel(logging.WARNING)
    logging.getLogger("nodriver").setLevel(logging.ERROR)


def metadata_client():
    from SpotiFLAC.core.spotify_metadata import SpotifyMetadataClient

    return SpotifyMetadataClient()


# ---------------------------------------------------------------------------
# search
# ---------------------------------------------------------------------------


async def _enrich_albums(client, albums, budget_s):
    """Fill in track_count and year for album hits.

    Spotify's search payload carries a release date for albums but no track
    count, and the track count is what the indexer turns into the release
    size and the `files` attr. One extra GraphQL call per album buys both;
    they run concurrently under a budget so a slow Spotify never stalls a
    Lidarr search past its own timeout. Un-enriched albums keep track_count
    0, which is exactly the old behaviour - degraded, not broken.
    """
    semaphore = asyncio.Semaphore(6)

    async def one(album):
        async with semaphore:
            try:
                info, tracks = await client.get_album_tracks_async(album["id"])
            except Exception:
                return
            album["track_count"] = len(tracks)
            if not album.get("release_date"):
                album["release_date"] = info.get("release_date", "")

    try:
        await asyncio.wait_for(
            asyncio.gather(*(one(a) for a in albums)),
            timeout=budget_s,
        )
    except Exception:
        # A slow or failing enrichment leaves track_count at 0, which is
        # the pre-enrichment behaviour; a Lidarr search must still answer.
        return


async def _search(query, limit, enrich, budget_s):
    client = metadata_client()
    results = await client.search_async(query, limit=limit)

    albums = [a for a in results.get("albums", []) if a.get("id")]
    if enrich and albums:
        await _enrich_albums(client, albums, budget_s)

    emitted = 0
    for album in albums:
        emit(
            "search_result",
            entity="album",
            name=album.get("name", ""),
            album=album.get("name", ""),
            artist=album.get("artists", ""),
            spotify_url=album.get(
                "external_url", f"https://open.spotify.com/album/{album['id']}"
            ),
            cover_url=album.get("cover_url", ""),
            year=release_year(album.get("release_date", "")),
            track_count=int(album.get("track_count", 0) or 0),
            isrc="",
            genre="",
        )
        emitted += 1

    for track in results.get("tracks", []):
        track_id = getattr(track, "id", "")
        if not track_id:
            continue
        emit(
            "search_result",
            entity="track",
            name=getattr(track, "title", ""),
            title=getattr(track, "title", ""),
            album=getattr(track, "album", ""),
            artist=getattr(track, "artists", ""),
            spotify_url=getattr(track, "external_url", "")
            or f"https://open.spotify.com/track/{track_id}",
            cover_url=getattr(track, "cover_url", ""),
            year=release_year(getattr(track, "release_date", "")),
            track_count=1,
            isrc=getattr(track, "isrc", "") or "",
            genre=getattr(track, "genre", "") or "",
        )
        emitted += 1

    return emitted


def run_search(args):
    quiet_loggers()
    try:
        emitted = asyncio.run(
            _search(args.search, args.limit, not args.no_enrich, args.enrich_budget)
        )
    except Exception as e:
        emit("error", message=f"search failed: {e}")
        return 1
    if emitted == 0:
        # Not an error: an unmatched query is a normal outcome, and the Go
        # side must not treat it as a dead backend and retry the CLI.
        emit("status", message="no results", query=args.search)
    return 0


# ---------------------------------------------------------------------------
# download
# ---------------------------------------------------------------------------


def resolve_release(url):
    """Best-effort album/track metadata before the download starts.

    Returns (artist, album, title, year, track_count). Every field is
    optional: metadata resolution failing must not stop a download that
    would otherwise work, so this degrades to empties.
    """
    try:
        client = metadata_client()
        name, tracks, _cover, info = client.get_url(url)
    except Exception:
        return "", "", "", 0, 0

    if not tracks:
        return "", "", name or "", release_year(info.get("release_date", "")), 0

    first = tracks[0]
    is_album = "/album/" in url
    artist = getattr(first, "album_artist", "") or getattr(first, "artists", "")
    album = getattr(first, "album", "") or (name if is_album else "")
    title = "" if is_album else getattr(first, "title", "")
    year = release_year(
        info.get("release_date", "") or getattr(first, "release_date", "")
    )
    return artist, album, title, year, len(tracks)


def emit_progress_until(stop, output_dir, expected):
    """Report progress from files landing on disk.

    SpotiFLAC's sync entry point is a blocking call with no progress
    callback, so the only observable progress is the output directory
    filling up. Without this the job sat at 0 % for its entire life and
    Lidarr's queue showed no movement at all.
    """
    last = -1
    while not stop.wait(2.0):
        done = len(audio_files(output_dir))
        if done == last:
            continue
        last = done
        percent = 100.0 * done / expected if expected > 0 else 0.0
        emit("progress", percent=round(min(percent, 99.0), 1), track=str(done))


def run_download(args):
    quality = normalize_quality(args.quality)
    services = [s.strip() for s in args.service.split(",") if s.strip()]

    emit("status", message="fetching metadata", url=args.url)
    quiet_loggers()

    try:
        from SpotiFLAC import SpotiFLAC
    except ImportError as e:
        emit("error", message=f"SpotiFLAC Python module not installed: {e}")
        return 1

    artist, album, title, year, track_count = resolve_release(args.url)
    if artist or album or title:
        emit(
            "metadata",
            artist=artist,
            album=album,
            title=title,
            year=year,
            track_count=track_count,
        )

    stop = threading.Event()
    progress = threading.Thread(
        target=emit_progress_until,
        args=(stop, args.output_dir, track_count),
        daemon=True,
    )
    progress.start()

    # Capture stderr for MusicBrainz metadata. Stdout noise (tqdm, emoji)
    # passes through - parseProgress ignores it.
    old_stderr = sys.stderr
    captured_stderr = io.StringIO()
    sys.stderr = captured_stderr

    import builtins

    original_input = builtins.input
    builtins.input = lambda prompt="": ""

    try:
        SpotiFLAC(
            url=args.url,
            output_dir=args.output_dir,
            services=services,
            quality=quality,
        )
    finally:
        builtins.input = original_input
        sys.stderr = old_stderr
        stop.set()
        progress.join(timeout=5)

    stderr_text = captured_stderr.getvalue()
    downloaded = audio_files(args.output_dir)

    if not downloaded:
        if "Verification required" in stderr_text or "challenge" in stderr_text.lower():
            emit("verification_required", url="", message="manual verification needed")
        # The provider cascade's own reason lines are the only explanation
        # there is for a failed download, and they only exist on the
        # captured stderr. Forwarding them turns "spotiflac exited: exit
        # status 1" into something diagnosable.
        emit(
            "error",
            message="no files downloaded - all services failed",
            detail=stderr_text[-2000:],
        )
        return 1

    if not artist and not title:
        artist, title = parse_filename_info(downloaded[0])

    for path in downloaded:
        emit(
            "track_done",
            track=os.path.basename(path),
            title=title,
            artist=artist,
            album=album,
            path=path,
        )

    # path is the directory, not a file: Lidarr imports the whole release
    # from whatever the download client reports as its storage location.
    emit(
        "complete",
        path=common_directory(downloaded, args.output_dir),
        size=total_size(downloaded),
        track_count=len(downloaded),
        artist=artist,
        album=album,
        title=title,
        year=year,
    )
    return 0


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--url")
    parser.add_argument("--search")
    parser.add_argument("--output-dir")
    parser.add_argument("--service", default="tidal,qobuz,deezer,amazon")
    parser.add_argument("--quality", default="LOSSLESS")
    parser.add_argument("--limit", type=int, default=20)
    parser.add_argument("--no-enrich", action="store_true")
    parser.add_argument("--enrich-budget", type=float, default=20.0)
    args = parser.parse_args()

    if args.search:
        return run_search(args)
    if not args.url or not args.output_dir:
        emit("error", message="either --search, or --url with --output-dir, is required")
        return 2
    return run_download(args)


if __name__ == "__main__":
    sys.exit(main())
