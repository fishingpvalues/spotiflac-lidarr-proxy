#!/usr/bin/env python3
"""Make SpotiFLAC's module-level asyncio locks per-event-loop.

SpotiFLAC 3.0.6 creates `_io_lock = asyncio.Lock()` at import time in
core/session_memory.py and core/profiles.py. An asyncio.Lock binds to
whichever loop first awaits it and rejects every other one, and SpotiFLAC
runs more than one loop per process - its sync entry points each call
asyncio.run(), and _run_async_sync spins up another in a worker thread. The
Deezer provider is the one that trips over it:

    ext:deezer  ·  Failed to resolve Deezer download:
                   <asyncio.locks.Lock object at 0x...> is bound to a
                   different event loop

That is a hard failure for that provider on every download, and there is no
newer release to upgrade to: 3.0.6 is the latest on PyPI.

The replacement keeps one real lock per running loop, so `async with
_io_lock:` behaves exactly as intended within any single loop and stops
raising across loops. Applied at image build against a pinned version, so it
is deterministic and visible rather than a silent monkeypatch - if the pin
moves and the shape changes, this fails loudly instead of quietly doing
nothing.
"""

import pathlib
import sys

SHIM = '''

class _PerLoopLock:
    """asyncio.Lock that binds per running event loop.

    Patched in by spotiflac-lidarr-proxy: see patches/python/per_loop_lock.py.
    """

    def __init__(self) -> None:
        self._locks: dict[object, "asyncio.Lock"] = {}

    def _for_current_loop(self) -> "asyncio.Lock":
        loop = asyncio.get_running_loop()
        lock = self._locks.get(loop)
        if lock is None:
            lock = asyncio.Lock()
            self._locks[loop] = lock
        return lock

    async def __aenter__(self):
        lock = self._for_current_loop()
        await lock.acquire()
        return lock

    async def __aexit__(self, exc_type, exc, tb):
        # Re-resolving is safe: the same loop always maps to the same lock.
        self._for_current_loop().release()
        return False


_io_lock = _PerLoopLock()
'''

TARGET = "_io_lock = asyncio.Lock()"


def patch(path: pathlib.Path) -> bool:
    text = path.read_text(encoding="utf-8")
    if "_PerLoopLock" in text:
        print(f"already patched: {path}")
        return True
    if TARGET not in text:
        print(f"PATTERN NOT FOUND in {path}", file=sys.stderr)
        return False
    # Drop the original binding and append the shim, so the name resolves to
    # the per-loop object without disturbing anything above it.
    text = text.replace(TARGET, "# _io_lock replaced below, see per_loop_lock.py", 1)
    path.write_text(text + SHIM, encoding="utf-8")
    print(f"patched: {path}")
    return True


def main() -> int:
    if len(sys.argv) < 2:
        print("usage: per_loop_lock.py <site-packages/SpotiFLAC>", file=sys.stderr)
        return 2
    root = pathlib.Path(sys.argv[1])
    targets = [root / "core" / "session_memory.py", root / "core" / "profiles.py"]
    ok = True
    for t in targets:
        if not t.exists():
            print(f"MISSING {t}", file=sys.stderr)
            ok = False
            continue
        ok = patch(t) and ok
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
