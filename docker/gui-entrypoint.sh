#!/bin/sh
set -e

Xvfb "$DISPLAY" -screen 0 1280x800x24 -nolisten tcp &
XVFB_PID=$!

# Give Xvfb a moment to bind before anything tries to connect to it.
sleep 2

fluxbox &

spotiflac-gui &

# This exposes a real interactive desktop, holding a real Spotify login
# session and community-verification tokens, over the network (Tailscale
# reaches every device on the tailnet, not just the operator's own). It must
# not be reachable without a password. Accept one via VNC_PASSWORD (so an
# operator can set it explicitly); generate a random one otherwise and print
# it to the container's own logs, not somewhere else, so it never ends up
# anywhere more exposed than this already-authenticated log access.
PASSFILE="$HOME/.vncpass"
if [ -z "$VNC_PASSWORD" ]; then
  VNC_PASSWORD=$(head -c 18 /dev/urandom | base64)
  echo "Generated VNC password (also required to view/control this session): $VNC_PASSWORD"
fi
x11vnc -storepasswd "$VNC_PASSWORD" "$PASSFILE"

# -localhost: only this container's own websockify (below) can reach x11vnc
# directly; nothing external connects to raw VNC on 5900 at all, only the
# browser-facing noVNC/websocket bridge on 6901.
x11vnc -display "$DISPLAY" -rfbauth "$PASSFILE" -localhost -forever -shared -rfbport 5900 -quiet &

trap 'kill $XVFB_PID 2>/dev/null' TERM INT

websockify --web=/usr/share/novnc 6901 localhost:5900
