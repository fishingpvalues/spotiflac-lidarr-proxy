#!/usr/bin/env python3
"""SpotiFLAC Python module wrapper — JSON progress output for Go proxy.

Usage:
  spotiflac-py-wrapper.py --url <spotify_url> --output-dir <dir>
                          [--service tidal,qobuz,deezer,amazon]
                          [--quality LOSSLESS]

Outputs JSON progress lines matching SpotiFLAC CLI format:
  {"type":"status","message":"..."}
  {"type":"metadata","artist":"...","album":"...","isrc":"..."}
  {"type":"progress","track":"...","percent":50}
  {"type":"complete","path":"...","size":12345}
  {"type":"error","message":"..."}
"""

import sys
import os
import json
import argparse
import traceback

def emit(event_type, **kwargs):
    kwargs["type"] = event_type
    print(json.dumps(kwargs), flush=True)

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--url", required=True)
    parser.add_argument("--output-dir", required=True)
    parser.add_argument("--service", default="tidal,qobuz,deezer,amazon")
    parser.add_argument("--quality", default="LOSSLESS")
    args = parser.parse_args()

    services = [s.strip() for s in args.service.split(",") if s.strip()]

    emit("status", message="fetching metadata", url=args.url)

    try:
        # Suppress noisy logs from SpotiFLAC internals
        import logging
        logging.getLogger().setLevel(logging.WARNING)
        logging.getLogger("SpotiFLAC").setLevel(logging.WARNING)
        logging.getLogger("nodriver").setLevel(logging.ERROR)

        from SpotiFLAC import SpotiFLAC

        # Suppress SpotiFLAC's noisy stdout/stderr and progress bars.
        # Redirect to /dev/null — we emit our own JSON progress.
        old_stdout = sys.stdout
        old_stderr = sys.stderr
        with open(os.devnull, 'w') as devnull:
            sys.stdout = devnull
            sys.stderr = devnull

            # Monkey-patch input() to auto-fail manual verification prompts
            # (Spotiflac falls back to other auth methods automatically).
            import builtins
            original_input = builtins.input
            builtins.input = lambda prompt="": ""

            try:
                instance = SpotiFLAC(
                    url=args.url,
                    output_dir=args.output_dir,
                    services=services,
                    quality=args.quality,
                )
            finally:
                builtins.input = original_input
                sys.stdout = old_stdout
                sys.stderr = old_stderr

        # Find downloaded files in output dir
        downloaded = []
        if os.path.isdir(args.output_dir):
            for root, dirs, files in os.walk(args.output_dir):
                for f in files:
                    if f.endswith(('.flac', '.mp3', '.m4a', '.alac', '.wav', '.ogg', '.opus')):
                        fp = os.path.join(root, f)
                        downloaded.append(fp)

        if downloaded:
            for fp in downloaded:
                size = os.path.getsize(fp)
                emit("track_done",
                     track=os.path.basename(fp),
                     title=os.path.splitext(os.path.basename(fp))[0],
                     path=fp)
            emit("complete",
                 path=downloaded[0],
                 size=os.path.getsize(downloaded[0]))
        else:
            # Check stderr for clues
            if "Verification required" in stderr_output or "Autenticazione" in stderr_output:
                emit("verification_required",
                     url="",
                     message="manual verification needed")
            emit("error", message="no files downloaded")

    except Exception as e:
        emit("error", message=str(e))
        traceback.print_exc(file=sys.stderr)

if __name__ == "__main__":
    main()
