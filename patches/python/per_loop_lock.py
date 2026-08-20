#!/usr/bin/env python3
"""Make SpotiFLAC's asyncio locks safe across event loops.

SpotiFLAC runs more than one event loop per process. Its sync entry points
each call asyncio.run(), _run_async_sync starts another in a worker thread,
and - the one that matters most here - extensions/runtime.py's
_handle_session_signed_fetch runs every session.signedFetch in "a NEW AND
ISOLATED asyncio event loop (asyncio.run)", once per request, and says so in
its own docstring.

An asyncio.Lock binds to whichever loop first awaits it and rejects every
other one, so any lock cached beyond the life of a single loop eventually
raises or hangs. Two places do that, and both break real downloads:

  core/session_memory.py, core/profiles.py
      `_io_lock = asyncio.Lock()` at import time. Surfaced as
      "ext:deezer - Failed to resolve Deezer download: <asyncio.locks.Lock
      object at 0x...> is bound to a different event loop".

  core/signed_session_mobile.py
      `_AUTH_LOCKS` keyed by namespace only, with a comment asserting that
      "no download starts its own asyncio.run()" - which the bridge above
      does, per request. Surfaced as "Bridge timeout for
      session.signedFetch".

3.0.6 is the newest release on PyPI, so there is nothing to upgrade to.

Applied at image build against a pinned version, and it exits non-zero when a
pattern is missing, so a future pin bump fails the build loudly instead of
silently patching nothing.
"""

import pathlib
import sys

SHIM = '''

class _PerLoopLock:
    """asyncio.Lock that binds per running event loop.

    Patched in by spotiflac-lidarr-proxy: see patches/python/per_loop_lock.py.
    """

    def __init__(self) -> None:
        # Weak keys, and keyed by the loop object rather than id(loop):
        # asyncio.run() frees its loop when it returns and CPython reuses the
        # address, so an id() key made the next loop collide with the previous
        # one's entry and hand back a lock bound to a closed loop.
        import weakref as _weakref

        self._locks = _weakref.WeakKeyDictionary()

    def _for_current_loop(self):
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

IO_LOCK_TARGET = "_io_lock = asyncio.Lock()"

AUTH_LOCK_OLD = """def _get_auth_lock(namespace: str) -> asyncio.Lock:
    \"\"\"Return the asyncio.Lock for the given namespace, creating it if absent.\"\"\"
    lock = _AUTH_LOCKS.get(namespace)
    if lock is None:
        lock = asyncio.Lock()
        _AUTH_LOCKS[namespace] = lock
    return lock
"""

AUTH_LOCK_NEW = """import weakref as _sfp_weakref

_AUTH_LOCKS_BY_LOOP = _sfp_weakref.WeakKeyDictionary()


def _get_auth_lock(namespace: str) -> asyncio.Lock:
    \"\"\"Return the asyncio.Lock for this namespace on the running loop.

    Patched by spotiflac-lidarr-proxy: keyed by loop as well as namespace,
    because extensions/runtime.py runs each session.signedFetch in its own
    asyncio.run(). Serialization within one loop is unchanged.
    \"\"\"
    loop = asyncio.get_running_loop()
    # Keyed by the loop object, not id(loop): asyncio.run() frees its loop and
    # CPython reuses the address, so an id() key collides with a previous,
    # already-closed loop and returns a lock bound to it.
    per_loop = _AUTH_LOCKS_BY_LOOP.get(loop)
    if per_loop is None:
        per_loop = {}
        _AUTH_LOCKS_BY_LOOP[loop] = per_loop
    lock = per_loop.get(namespace)
    if lock is None:
        lock = asyncio.Lock()
        per_loop[namespace] = lock
    return lock
"""


def patch_io_lock(path: pathlib.Path) -> bool:
    text = path.read_text(encoding="utf-8")
    if "_PerLoopLock" in text:
        print(f"already patched: {path}")
        return True
    if IO_LOCK_TARGET not in text:
        print(f"PATTERN NOT FOUND (_io_lock) in {path}", file=sys.stderr)
        return False
    text = text.replace(
        IO_LOCK_TARGET, "# _io_lock replaced below, see per_loop_lock.py", 1
    )
    path.write_text(text + SHIM, encoding="utf-8")
    print(f"patched _io_lock: {path}")
    return True


def patch_auth_locks(path: pathlib.Path) -> bool:
    text = path.read_text(encoding="utf-8")
    if "keyed by loop as well as namespace" in text:
        print(f"already patched: {path}")
        return True
    if AUTH_LOCK_OLD not in text:
        print(f"PATTERN NOT FOUND (_get_auth_lock) in {path}", file=sys.stderr)
        return False
    path.write_text(text.replace(AUTH_LOCK_OLD, AUTH_LOCK_NEW, 1), encoding="utf-8")
    print(f"patched _get_auth_lock: {path}")
    return True



# ---------------------------------------------------------------------------
# Bridge and extension-call timeouts
# ---------------------------------------------------------------------------
#
# The extension stack nests three budgets, and the outer two are smaller than
# the innermost operation needs:
#
#   extensions/_bridge.js   waits 60s for the Python session.signedFetch handler
#   extensions/runtime.py   waits 120s for a JS extension call
#   core/signed_session_mobile.perform_signed_fetch   allows itself 300s, and
#                           drives a Turnstile solve that genuinely takes minutes
#
# So a signed fetch that needs a Turnstile can never finish: the bridge gives
# up at 60s and the caller at 120s. That is precisely what every provider
# reported -
#
#   ext:tidal-web · NETWORK_ERROR: Timeout (120s) calling download
#   ext:qobuz-web · NETWORK_ERROR: Timeout (120s) calling checkAvailability
#   ext:deezer    · Bridge timeout for session.signedFetch
#
# - three different messages, one cause. The budgets are raised to sit outside
# the 300s inner one rather than inside it.

BRIDGE_TIMEOUT_OLD = "if (waited > 60_000) throw new Error(`Bridge timeout for ${method}`);"
BRIDGE_TIMEOUT_NEW = (
    "if (waited > 600_000) throw new Error(`Bridge timeout for ${method}`);"
    "  // patched by spotiflac-lidarr-proxy: was 60_000, shorter than the"
    " 300s signed-fetch budget it waits on"
)

CALL_TIMEOUT_OLD = "        timeout: float = 120.0,"
CALL_TIMEOUT_NEW = (
    "        # patched by spotiflac-lidarr-proxy: was 120.0, shorter than the\n"
    "        # 300s budget perform_signed_fetch allows itself downstream.\n"
    "        timeout: float = 900.0,"
)



# ---------------------------------------------------------------------------
# Turnstile solve budget in core/solver.py
# ---------------------------------------------------------------------------
#
# _RELOAD_CHECK_SECONDS = 10.0 with _MAX_RELOAD_ATTEMPTS = 3 gives a Turnstile
# solve 30 seconds in total, in 10-second slices:
#
#   per_attempt_seconds = min(_RELOAD_CHECK_SECONDS, float(timeout)) ...
#
# so the 300s budget the caller passes down is capped at 10 regardless. A real
# Turnstile in a headless-ish browser routinely needs longer than that, and the
# result is
#
#   ext:deezer · Turnstile token non ottenuto dopo 3 tentativi (10s ciascuno)
#
# which is what remained once the bridge and extension-call timeouts stopped
# firing first. 60s per attempt keeps the retry-and-reload behaviour, still
# fits inside the 300s inner budget with three attempts, and gives the solve
# a realistic chance.

RELOAD_SECONDS_OLD = "_RELOAD_CHECK_SECONDS = 10.0"
RELOAD_SECONDS_NEW = (
    "_RELOAD_CHECK_SECONDS = 60.0"
    "  # patched by spotiflac-lidarr-proxy: was 10.0, which capped a"
    " Turnstile solve at 3x10s"
)


# ---------------------------------------------------------------------------
# JSExtensionProvider call timeout in extensions/provider.py
# ---------------------------------------------------------------------------
#
# provider.py hands its own timeout to every extension call
#
#   rt.call(method, *args, timeout=self._timeout_s, **kw)
#
# and self._timeout_s defaults to 120 in two places, with nothing passing it
# explicitly. So raising JSRuntime.call's own default was not enough - the
# provider kept overriding it, and downloads still failed with
#
#   ext:deezer · NETWORK_ERROR: Timeout (120s) calling download
#
# even after the Turnstile solve was given room to finish. Both defaults go up
# together.

PROVIDER_TIMEOUT_CTOR_OLD = "        timeout_s: int = 120,\n        **kwargs,"
PROVIDER_TIMEOUT_CTOR_NEW = (
    "        # patched by spotiflac-lidarr-proxy: was 120, shorter than a\n"
    "        # Turnstile-gated download needs.\n"
    "        timeout_s: int = 900,\n        **kwargs,"
)

PROVIDER_TIMEOUT_FACTORY_OLD = (
    "    node_executable: str = \"node\",\n    timeout_s: int = 120,\n)"
)
PROVIDER_TIMEOUT_FACTORY_NEW = (
    "    node_executable: str = \"node\",\n"
    "    # patched by spotiflac-lidarr-proxy: was 120.\n"
    "    timeout_s: int = 900,\n)"
)

def patch_literal(path: pathlib.Path, old: str, new: str, marker: str, label: str) -> bool:
    text = path.read_text(encoding="utf-8")
    if marker in text:
        print(f"already patched ({label}): {path}")
        return True
    if old not in text:
        print(f"PATTERN NOT FOUND ({label}) in {path}", file=sys.stderr)
        return False
    path.write_text(text.replace(old, new, 1), encoding="utf-8")
    print(f"patched {label}: {path}")
    return True


def main() -> int:
    if len(sys.argv) < 2:
        print("usage: per_loop_lock.py <site-packages/SpotiFLAC>", file=sys.stderr)
        return 2
    root = pathlib.Path(sys.argv[1])
    ok = True

    for target in (root / "core" / "session_memory.py", root / "core" / "profiles.py"):
        if not target.exists():
            print(f"MISSING {target}", file=sys.stderr)
            ok = False
            continue
        ok = patch_io_lock(target) and ok

    mobile = root / "core" / "signed_session_mobile.py"
    if not mobile.exists():
        print(f"MISSING {mobile}", file=sys.stderr)
        ok = False
    else:
        ok = patch_auth_locks(mobile) and ok

    bridge = root / "extensions" / "_bridge.js"
    if not bridge.exists():
        print(f"MISSING {bridge}", file=sys.stderr)
        ok = False
    else:
        ok = patch_literal(
            bridge, BRIDGE_TIMEOUT_OLD, BRIDGE_TIMEOUT_NEW,
            "600_000", "bridge timeout",
        ) and ok

    runtime = root / "extensions" / "runtime.py"
    if not runtime.exists():
        print(f"MISSING {runtime}", file=sys.stderr)
        ok = False
    else:
        ok = patch_literal(
            runtime, CALL_TIMEOUT_OLD, CALL_TIMEOUT_NEW,
            "timeout: float = 900.0", "extension call timeout",
        ) and ok

    solver = root / "core" / "solver.py"
    if not solver.exists():
        print(f"MISSING {solver}", file=sys.stderr)
        ok = False
    else:
        ok = patch_literal(
            solver, RELOAD_SECONDS_OLD, RELOAD_SECONDS_NEW,
            "_RELOAD_CHECK_SECONDS = 60.0", "turnstile solve budget",
        ) and ok

    provider = root / "extensions" / "provider.py"
    if not provider.exists():
        print(f"MISSING {provider}", file=sys.stderr)
        ok = False
    else:
        text = provider.read_text(encoding="utf-8")
        if "timeout_s: int = 900" in text:
            print(f"already patched (provider timeouts): {provider}")
        else:
            before = text
            text = text.replace("timeout_s: int = 120,", "timeout_s: int = 900,  # patched by spotiflac-lidarr-proxy: was 120")
            if text == before:
                print(f"PATTERN NOT FOUND (provider timeouts) in {provider}", file=sys.stderr)
                ok = False
            else:
                provider.write_text(text, encoding="utf-8")
                print(f"patched provider timeouts: {provider}")

    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
