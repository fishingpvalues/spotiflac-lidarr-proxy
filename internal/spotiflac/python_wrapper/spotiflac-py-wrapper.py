#!/usr/bin/env python3
"""SpotiFLAC Python module wrapper — JSON progress output for Go proxy.

Outputs JSON lines matching SpotiFLAC CLI format.
SpotiFLAC noise on stdout is harmless — Go proxy's parseProgress
ignores non-JSON lines.
"""

import sys
import os
import json
import argparse
import logging
import re
import io

def emit(event_type, **kwargs):
    kwargs["type"] = event_type
    print(json.dumps(kwargs), flush=True)

def parse_filename_info(filepath):
    """Extract artist/title from filename like 'Title - Artist.flac'."""
    basename = os.path.splitext(os.path.basename(filepath))[0]
    parts = basename.split(" - ", 1)
    if len(parts) == 2:
        return parts[1].strip(), parts[0].strip()
    return "", basename

def extract_metadata_from_logs(text):
    """Parse SpotiFLAC's MusicBrainz output for artist/album/title/isrc."""
    meta = {}
    for pattern, key in [
        (r'artist(?:\s*\(sort\))?:\s*([^,\n]+)', "artist"),
        (r'title:\s*([^,\n]+)', "title"),
        (r'album:\s*([^,\n]+)', "album"),
        (r'isrc:\s*([A-Z]{2}-?[A-Z0-9]{3}-?\d{2}-?\d{5})', "isrc"),
    ]:
        m = re.search(pattern, text, re.IGNORECASE)
        if m:
            meta[key] = m.group(1).strip()
    return meta

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--url", required=True)
    parser.add_argument("--output-dir", required=True)
    parser.add_argument("--service", default="tidal,qobuz,deezer,amazon")
    parser.add_argument("--quality", default="LOSSLESS")
    args = parser.parse_args()

    services = [s.strip() for s in args.service.split(",") if s.strip()]

    emit("status", message="fetching metadata", url=args.url)

    logging.getLogger().setLevel(logging.WARNING)
    logging.getLogger("SpotiFLAC").setLevel(logging.WARNING)
    logging.getLogger("nodriver").setLevel(logging.ERROR)

    try:
        from SpotiFLAC import SpotiFLAC
    except ImportError as e:
        emit("error", message=f"SpotiFLAC Python module not installed: {e}")
        sys.exit(1)

    # Capture stderr for MusicBrainz metadata.
    # Stdout noise (tqdm, emoji) passes through — parseProgress ignores it.
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
            quality=args.quality,
        )
    finally:
        builtins.input = original_input
        sys.stderr = old_stderr

    stderr_text = captured_stderr.getvalue()
    log_meta = extract_metadata_from_logs(stderr_text)

    # Find downloaded files
    downloaded = []
    if os.path.isdir(args.output_dir):
        for root, dirs, files in os.walk(args.output_dir):
            for f in files:
                if f.endswith(('.flac', '.mp3', '.m4a', '.alac', '.wav', '.ogg', '.opus')):
                    downloaded.append(os.path.join(root, f))

    if not downloaded:
        if "Verification required" in stderr_text or "challenge" in stderr_text.lower():
            emit("verification_required", url="", message="manual verification needed")
        emit("error", message="no files downloaded — all services failed")
        sys.exit(1)

    filename_artist, filename_title = parse_filename_info(downloaded[0])
    artist = log_meta.get("artist") or filename_artist
    title = log_meta.get("title") or filename_title
    album = log_meta.get("album") or ""
    isrc = log_meta.get("isrc") or ""

    for fp in downloaded:
        basename = os.path.basename(fp)

        emit("metadata", artist=artist, album=album, title=title, isrc=isrc)

        emit("track_done",
             track=basename, title=title, artist=artist, album=album, path=fp)

    emit("complete",
         path=downloaded[0], size=os.path.getsize(downloaded[0]),
         artist=artist, album=album, title=title)

if __name__ == "__main__":
    main()
