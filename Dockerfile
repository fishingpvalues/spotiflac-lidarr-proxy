# Stage 1: Build proxy server
FROM golang:1.25-alpine AS builder
# "develop" (not e.g. "dev"), unless overridden by --build-arg VERSION=vX.Y.Z
# (as release.yml/beta.yml do): Lidarr's SABnzbd client special-cases the
# literal string "develop" to assume SABnzbd 3.0.0+, and rejects anything
# else that isn't a strict semver-shaped X.Y.Z. See cmd/server/main.go.
ARG VERSION=develop
WORKDIR /build
RUN apk add --no-cache git ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=${VERSION}" -o /out/server ./cmd/server

# Stage 2: Build spotiflac-cli from fork (headless, relay-capable)
FROM golang:1.26-alpine AS cli-builder
# a74d185 fixes the headless CLI dying on the first metadata batch of any
# album URL: app.go emitted Wails frontend events inline, and the CLI's
# context.Background() is not a Wails lifecycle context, so EventsEmit killed
# the process. Verified on the target deployment - the pinned build exits 1
# after one stdout line for an album URL, a74d185 runs on to the provider
# cascade. Album downloads through backends 2-4 had never worked.
ARG SPOTIFLAC_COMMIT=a74d1856d3159477dd131cd58067f2089e936a24
RUN apk add --no-cache git
RUN git clone https://github.com/fishingpvalues/SpotiFLAC.git /spotiflac && \
    cd /spotiflac && git checkout ${SPOTIFLAC_COMMIT}
WORKDIR /spotiflac
RUN CGO_ENABLED=0 go build -tags headless -ldflags="-s -w" -o /out/spotiflac-cli .

# Stage 3: Runtime
FROM alpine:3.21

# UID/GID are build args, and the user is a NORMAL user with an explicit id,
# not a system user from `adduser -S`.
#
# `adduser -S` picked whatever id was free (100:101 in practice) while every
# deployment runs the container as `user: "1000:1000"` to match the host's
# media ownership. $HOME then belonged to uid 100 and was NOT writable by the
# process, and that single fact broke the entire Python backend: Chromium
# cannot create its crashpad database under an unwritable HOME and dies with
# SIGTRAP ("chrome_crashpad_handler: --database is required", then "Trace/
# breakpoint trap"), so pydoll reports "Browser failed to start within
# timeout", the tidal-web and qobuz-web extensions time out after 120s, and
# every download fails with a bare "exit status 1". Verified both ways in a
# running container: as uid 0, or as uid 1000 with HOME pointed at a writable
# directory, the same Chromium command exits 0 and prints the DOM.
ARG PUID=1000
ARG PGID=1000

# chromium/nodejs/xvfb are what the Python backend actually runs: SpotiFLAC's
# extensions are node scripts, and its Turnstile solver drives Chromium under
# Xvfb through pydoll. None of them were in this image, so the published
# image could not run the backend it makes priority 1 - and a hand-installed
# `apk add` inside a live container disappears on the next recreate.
RUN apk add --no-cache \
        ca-certificates tzdata tini \
        chromium nodejs xvfb font-noto ttf-freefont && \
    addgroup -g ${PGID} spotiflac && \
    adduser -D -u ${PUID} -G spotiflac spotiflac

# The Python backend is what the proxy tries first and what actually produces
# files in practice. It used to be left to whoever built a downstream image,
# so the published image did not contain the backend its own documentation
# called priority 1 - anyone pulling it silently got the CLI backends only.
#
# Built here rather than copied from python:3.12-alpine: that image keeps its
# interpreter in /usr/local/bin, so a venv created there points its symlinks
# and pyvenv.cfg at paths this image does not have.
#
# pydoll: SpotiFLAC imports it unconditionally for the Amazon provider, so
# without it the entire module fails to import and every backend-1 download
# fails. The distribution is named "pydoll-python" while the module is
# "pydoll", which is why it was previously believed to be missing from PyPI
# and replaced with hand-written stub modules. It is a real package, so the
# stubs are gone. Amazon additionally needs a browser this image does not
# ship, which is why it is not in the default fallback chain.
# SpotiFLAC is pinned. It was unpinned, so every rebuild silently picked up
# whatever PyPI had latest - which is both irreproducible and impossible to
# patch against, since a patch has to match a known shape.
#
# The patch itself: SpotiFLAC 3.0.6 creates `_io_lock = asyncio.Lock()` at
# import time in core/session_memory.py and core/profiles.py. An asyncio.Lock
# binds to the first loop that awaits it and rejects every other one, and
# SpotiFLAC runs several loops per process, so the Deezer provider failed on
# every single download with
#
#   ext:deezer · Failed to resolve Deezer download: <asyncio.locks.Lock
#                object at 0x...> is bound to a different event loop
#
# 3.0.6 is the newest release, so there is nothing to upgrade to. The patch
# makes those locks per-loop; verified in a live container, where the same
# lock is now usable from two consecutive asyncio.run() calls that previously
# raised. It fails the build loudly if the pattern is gone, rather than
# quietly doing nothing after a version bump.
#
# provider_breaker.py adds fast cross-service failover to the downloader:
# without it one dead service holds every track for its full per-track budget
# and is re-tried with a fresh budget on every subsequent track - measured
# against a real outage, a 2-track album burned the proxy's whole 30m job
# budget before the Go-side fallback chain could start. The patch caps each
# provider's share of a track (SPOTIFLAC_PROVIDER_BUDGET_S, default 300s)
# and skips a provider for the rest of the download once it has failed
# (SPOTIFLAC_PROVIDER_BREAKER_FAILURES, default 1).
COPY patches/python/per_loop_lock.py /tmp/per_loop_lock.py
COPY patches/python/provider_breaker.py /tmp/provider_breaker.py
RUN apk add --no-cache python3 py3-pip && \
    python3 -m venv /venv && \
    /venv/bin/pip install --no-cache-dir \
        "SpotiFLAC==3.0.6" requests nodriver pydoll-python && \
    /venv/bin/python3 /tmp/per_loop_lock.py \
        /venv/lib/python3.12/site-packages/SpotiFLAC && \
    /venv/bin/python3 /tmp/provider_breaker.py \
        /venv/lib/python3.12/site-packages/SpotiFLAC && \
    rm -f /tmp/per_loop_lock.py /tmp/provider_breaker.py && \
    apk del py3-pip && \
    find /venv -name '__pycache__' -type d -prune -exec rm -rf {} +

COPY --from=builder /out/server /usr/local/bin/server
COPY --from=cli-builder /out/spotiflac-cli /usr/local/bin/spotiflac-cli
RUN mkdir -p /downloads /data /home/spotiflac/.spotiflac /home/spotiflac/.cache/spotiflac && \
    chown -R spotiflac:spotiflac /downloads /data /home/spotiflac /venv
USER spotiflac
ENV HOME=/home/spotiflac \
    SPF_SPOTIFLAC_PYTHON_VENV=/venv/bin/python3 \
    CHROME_BIN=/usr/bin/chromium-browser
EXPOSE 8484
# tini, because PID 1 has to reap. Chromium forks a tree of helper processes
# per browser, and when a solve dies its orphans are reparented to PID 1 -
# which is this Go server, and a Go binary never calls wait(). Measured in a
# live container: 47 <defunct> chromium entries after a few hours of jobs,
# each one holding a PID table slot until the container is recreated. tini
# reaps them; the server keeps running as its child and still receives
# signals, so shutdown behaviour is unchanged.
ENTRYPOINT ["/sbin/tini", "--", "server", "serve"]
