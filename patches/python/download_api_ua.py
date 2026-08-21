#!/usr/bin/env python3
"""Stop the signed-session client identifying itself as SpotiFLAC.

The stock client sends ``User-Agent: SpotiFLAC-Mobile/<version>`` on every
request to the download APIs (zarz.moe bootstrap/exchange/refresh and the
signed track-download fetches that ride the same httpx client). Measured
live from a VPN egress on 2026-08-21:

  * zarz.moe answers **403 Forbidden** to that User-Agent on /bootstrap -
    before any Turnstile challenge is even offered;
  * the same endpoint answers **200 + a solvable Turnstile challenge** to a
    browser User-Agent from the identical IP.

So the mobile identifier is now treated as a bot signature by the
Cloudflare-fronted download APIs. With the stock header every signed session
dies at bootstrap, which fails ALL extension providers (tidal-web, qobuz-web)
regardless of whether their Turnstile solving would have worked.

The patch makes the header overridable via SPOTIFLAC_DOWNLOAD_API_UA and
defaults it to a desktop Chrome User-Agent matching the Chromium build in
the image. Set the env var back to "SpotiFLAC-Mobile/1.0" if an API ever
starts requiring the original identity again.

Applied at image build against a pinned version; exits non-zero when a
pattern is missing so a future pin bump fails the build loudly instead of
silently patching nothing.
"""

import pathlib
import sys


def replace_once(path: pathlib.Path, old: str, new: str, label: str) -> bool:
    """Replace old with new exactly once; idempotent via a marker comment."""
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

UA_OLD = '''        self._client_headers = {
            **_BROWSER_FINGERPRINT_HEADERS,
            # Do not include Origin by default; observed captures omit it for
            # these signed requests coming from the extension runtime.
            "User-Agent": f"SpotiFLAC-Mobile/{self.app_version}",
        }'''

UA_NEW = '''        self._client_headers = {
            **_BROWSER_FINGERPRINT_HEADERS,
            # Do not include Origin by default; observed captures omit it for
            # these signed requests coming from the extension runtime.
            # PATCHED_MARKER_download_api_ua
            # Patched by spotiflac-lidarr-proxy: the stock mobile identifier
            # is 403-blocked by Cloudflare-fronted download APIs (measured:
            # zarz.moe refuses "SpotiFLAC-Mobile/*" but serves a solvable
            # Turnstile challenge to a browser User-Agent from the same IP).
            # Overridable via SPOTIFLAC_DOWNLOAD_API_UA.
            "User-Agent": os.environ.get(
                "SPOTIFLAC_DOWNLOAD_API_UA",
                "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 "
                "(KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36",
            ),
        }'''


def main() -> int:
    if len(sys.argv) != 2:
        print(f"usage: {sys.argv[0]} <SpotiFLAC-package-dir>", file=sys.stderr)
        return 2
    root = pathlib.Path(sys.argv[1])
    target = root / "core" / "signed_session_mobile.py"
    if not target.is_file():
        print(f"missing {target}", file=sys.stderr)
        return 1

    ok = replace_once(target, UA_OLD, UA_NEW, "download_api_ua")

    # os must be importable where we use it; it already is (os.path.expanduser
    # is used in __init__), but fail loudly if a future refactor drops it.
    if ok and "import os" not in target.read_text(encoding="utf-8"):
        print("FATAL: 'import os' missing from signed_session_mobile.py", file=sys.stderr)
        return 1
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
