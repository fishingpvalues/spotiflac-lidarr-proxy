#!/usr/bin/env python3
"""Turnstile challenge solver v2 - vision LLM decides, accessibility tree aims.

Runs INSIDE the spotiflac-proxy container (chromium + Xvfb are there).
Stdlib-only: the image's python3 has NO third-party packages, so CDP talks
through a minimal RFC6455 client below. Sequential send->recv-matching is
deliberate - the async reader-task variant wedged two solver runs (see
docs/SPOTIFLAC_LIDARR_PROXY.md pitfalls).

Design (v2, 2026-08-22):
  * The VLM (mittwald Qwen3.5-122B, vision + tool calling) is a STATE JUDGE:
    it sees a screenshot + the live iframe text and answers click / wait /
    done. It no longer guesses pixel coordinates (that produced malformed
    tool args AND imprecise targets).
  * Click coordinates come from the ACCESSIBILITY TREE of the Turnstile
    OOPIF target (Accessibility.getFullAXTree -> role=checkbox ->
    DOM.getContentQuads), mapped to page space via Page.getFrameOwner +
    DOM.getBoxModel - the exact plumbing solve12.py proved live.
  * Interactions are shaped to look human: bezier mouse trajectory with
    variable step timing, pre-click dwell, off-center aim, variable
    inter-action delays, keyboard Tab+Space fallback, persistent browser
    profile across runs, navigator.webdriver masked.
  * Network events are captured: a real accepted click produces traffic to
    challenges.cloudflare.com; session 3 measured ZERO such POSTs after
    precise clicks from this egress (CF hold), so the log distinguishes
    "clicked, nothing happened" from "clicked, verification started".

Flow:
  1. mint a fresh spotbye challenge with a loopback cb pointing at our own
     capture server (the grant lands here, not in a dead port)
  2. load the challenge in a real (non-headless) chromium under Xvfb
  3. loop: observe (screenshot + iframe AX/text) -> VLM decides -> act
  4. on cb redirect: capture the grant URL, exchange it for a community
     session, write BOTH desktop stores (Go CLI + python)

Wiring: the server calls this automatically when the community session store
holds nothing valid, via SPF_SESSION_RENEW_CMD (see README, "Session
renewal"). Nothing runs it unless that variable is set, and the client
rate-limits it with SPF_SESSION_RENEW_MIN_INTERVAL_S.

  SPF_SESSION_RENEW_CMD=python3 /opt/solver/turnstile-llm-solve.py 10
  TURNSTILE_LLM_API_KEY=<key for the vision model>

Manual run (same thing, for debugging):
  docker exec -e TURNSTILE_LLM_API_KEY=$K spotiflac-proxy \
      sh -c "python3 /opt/solver/turnstile-llm-solve.py 10"

Exit codes: 0 session written | 3 no grant (wall/hold) | 2 crash
HARD CONSTRAINT: every request leaves through the VPN tunnel (we run inside
gluetun's netns) - do not "fix" slowness by moving this outside the container.
"""
import base64
import json
import math
import os
import random
import re
import secrets
import socket
import struct
import subprocess
import sys
import threading
import time
import traceback
import urllib.parse
import urllib.request
from http.server import BaseHTTPRequestHandler, HTTPServer

VERIFY_BASE = os.environ.get("TURNSTILE_VERIFY_BASE", "https://verify.spotbye.qzz.io")
UA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/136 Safari/537.36"
LLM_BASE = os.environ.get("TURNSTILE_LLM_BASE", "https://llm.aihosting.mittwald.de/v1").rstrip("/")
LLM_MODEL = os.environ.get("TURNSTILE_LLM_MODEL", "Qwen3.5-122B-A10B-FP8")

# Session stores. The Go CLI reads SPF_COMMUNITY_SESSION_FILE (see
# internal/spotiflac/client.go communitySessionFile), the Python extensions
# read their own signed_sessions copy. Both are written on a successful
# renewal, because a session only one of them can see is a session that
# silently does not work for half the pipeline.
SPOTIFLAC_HOME = os.environ.get("SPOTIFLAC_HOME") or os.path.join(
    os.environ.get("HOME", "/home/spotiflac"), ".spotiflac")
CLI_STORE = os.environ.get("SPF_COMMUNITY_SESSION_FILE") or os.path.join(
    SPOTIFLAC_HOME, "community_session.json")
PY_STORE = os.path.join(SPOTIFLAC_HOME, "signed_sessions", "community_sessions.json")


def _install_id():
    """Stable install id for this deployment.

    spotbye ties a session to the install_id that bootstrapped its challenge,
    so this must be the SAME id the CLI already uses - a fresh one produces a
    session the CLI store accepts and the API rejects. Order: explicit env,
    then whatever the existing store says, then a new random id persisted by
    the successful-renewal write at the end.
    """
    env = os.environ.get("TURNSTILE_INSTALL_ID", "").strip()
    if env:
        return env
    for path in (CLI_STORE, PY_STORE):
        try:
            with open(path) as fh:
                got = json.load(fh).get("install_id", "").strip()
            if got:
                return got
        except (OSError, ValueError, AttributeError):
            continue
    return secrets.token_hex(16)


CLI_INSTALL_ID = _install_id()
DEBUG_PORT = 9344
CAP_PORT = 39786
# Persistent profile ON PURPOSE: fresh profiles look bot-like; reusing one
# across runs carries cookie/UA history forward (automation-plan step 3a).
PROFILE = os.environ.get("TURNSTILE_PROFILE", "/tmp/llm_profile_persistent")
GRANT_LOG = "/tmp/grant_capture_llm.log"
MAX_ROUNDS = int(sys.argv[1]) if len(sys.argv) > 1 else 10

API_KEY = os.environ.get("TURNSTILE_LLM_API_KEY") or os.environ.get("MITTWALD_API_KEY", "")


def log(msg):
    print(time.strftime("%H:%M:%S"), msg, flush=True)


def jrand(lo, hi):
    return random.uniform(lo, hi)


# ------------------------------------------------------- minimal websocket ---
class WsClient:
    """Just enough RFC6455 for CDP: text frames both ways, 64-bit lengths."""

    def __init__(self, url, timeout=60):
        m = re.match(r"ws://([^:/]+):(\d+)(/.*)$", url)
        if not m:
            raise ValueError("unsupported ws url: %s" % url)
        host, port, path = m.group(1), int(m.group(2)), m.group(3)
        self.sock = socket.create_connection((host, port), timeout=timeout)
        key = base64.b64encode(secrets.token_bytes(16)).decode()
        req = ("GET %s HTTP/1.1\r\nHost: %s:%d\r\nUpgrade: websocket\r\n"
               "Connection: Upgrade\r\nSec-WebSocket-Key: %s\r\n"
               "Sec-WebSocket-Version: 13\r\n\r\n") % (path, host, port, key)
        self.sock.sendall(req.encode())
        resp = b""
        while b"\r\n\r\n" not in resp:
            chunk = self.sock.recv(4096)
            if not chunk:
                raise RuntimeError("ws handshake closed early")
            resp += chunk
        if b"101" not in resp.split(b"\r\n", 1)[0]:
            raise RuntimeError("ws handshake failed: %r" % resp[:120])
        self._mid = 0
        self.on_event = None  # callback(event_dict) for id-less messages

    def _recv_exact(self, n):
        buf = b""
        while len(buf) < n:
            chunk = self.sock.recv(n - len(buf))
            if not chunk:
                raise RuntimeError("ws connection closed")
            buf += chunk
        return buf

    def recv_frame(self):
        while True:
            b1, b2 = self._recv_exact(2)
            fin_op, mask_bit = b1, b2
            opcode = fin_op & 0x0F
            length = mask_bit & 0x7F
            if length == 126:
                length = struct.unpack(">H", self._recv_exact(2))[0]
            elif length == 127:
                length = struct.unpack(">Q", self._recv_exact(8))[0]
            payload = self._recv_exact(length) if length else b""
            if opcode == 0x8:  # close
                raise RuntimeError("ws closed by peer")
            if opcode == 0x9:  # ping -> pong
                self._send_raw(0xA, payload)
                continue
            if opcode in (0x1, 0x2):  # text/binary
                return payload

    def _send_raw(self, opcode, payload):
        header = bytes([0x80 | opcode])
        n = len(payload)
        if n < 126:
            header += bytes([0x80 | n])
        elif n < 1 << 16:
            header += bytes([0x80 | 126]) + struct.pack(">H", n)
        else:
            header += bytes([0x80 | 127]) + struct.pack(">Q", n)
        mask = secrets.token_bytes(4)
        masked = bytes(b ^ mask[i % 4] for i, b in enumerate(payload))
        self.sock.sendall(header + mask + masked)

    def send_json(self, obj):
        self._send_raw(0x1, json.dumps(obj).encode())

    def cdp(self, method, params=None, timeout=60):
        self._mid += 1
        mid = self._mid
        self.send_json({"id": mid, "method": method, "params": params or {}})
        deadline = time.time() + timeout
        while True:
            if time.time() > deadline:
                raise TimeoutError("CDP %s timed out" % method)
            raw = self.recv_frame().decode("utf-8", "replace")
            try:
                msg = json.loads(raw)
            except json.JSONDecodeError:
                continue
            if msg.get("id") == mid:
                if "error" in msg:
                    raise RuntimeError("CDP error %s: %s" % (method, msg["error"]))
                return msg.get("result", {})
            if "id" not in msg and self.on_event:
                try:
                    self.on_event(msg)
                except Exception:
                    pass

    def close(self):
        try:
            self.sock.close()
        except Exception:
            pass


# ---------------------------------------------------------------- capture ---
class Cap(BaseHTTPRequestHandler):
    def do_GET(self):
        with open(GRANT_LOG, "a") as f:
            f.write(self.path + "\n")
        self.send_response(200)
        self.send_header("Content-Type", "text/html")
        self.end_headers()
        self.wfile.write(b"<html><body>Verified</body></html>")

    def log_message(self, *a):
        pass


srv = HTTPServer(("127.0.0.1", CAP_PORT), Cap)
threading.Thread(target=srv.serve_forever, daemon=True).start()
log(f"capture server on {CAP_PORT}")


# ------------------------------------------------------------- challenge ----
def mint_challenge(cb):
    req = urllib.request.Request(
        f"{VERIFY_BASE}/bootstrap?install_id={CLI_INSTALL_ID}&app_version=unknown&platform=desktop",
        headers={"User-Agent": UA})
    d = json.load(urllib.request.urlopen(req, timeout=20))
    base_url, _sep, query = d["challenge_url"].partition("?")
    params = dict(urllib.parse.parse_qsl(query))
    params["cb"] = cb
    url = base_url + "?" + urllib.parse.urlencode(params)
    open("/tmp/llm_challenge.txt", "w").write(url)
    return url


CB = f"http://127.0.0.1:{CAP_PORT}/session-grant?state=llmsolve{os.getpid()}"
CHALLENGE = mint_challenge(CB)
log("challenge minted")


# --------------------------------------------------------------- chromium ---
def detect_display():
    env = os.environ.get("DISPLAY")
    if env:
        return env
    try:
        out = subprocess.run(["ps", "aux"], capture_output=True, text=True, timeout=5).stdout
        for line in out.splitlines():
            if "Xvfb" in line:
                for tok in line.split():
                    if re.fullmatch(r":\d+", tok):
                        return tok
    except Exception:
        pass
    return ":99"


DISPLAY = detect_display()
log(f"using DISPLAY {DISPLAY}, profile {PROFILE}")
chromium = subprocess.Popen([
    "/usr/bin/chromium", f"--remote-debugging-port={DEBUG_PORT}",
    f"--user-data-dir={PROFILE}",
    "--no-sandbox", "--disable-setuid-sandbox", "--disable-dev-shm-usage",
    "--use-gl=disabled",  # REQUIRED: without it the GPU process dies under
    # Xvfb and Page.enable hangs forever (measured 2026-08-21)
    "--ozone-platform-hint=auto", "--window-size=1280,900",
    "--force-device-scale-factor=1",  # screenshot px == CDP input px
    "--disable-blink-features=AutomationControlled",
    "--disable-background-timer-throttling", "--disable-backgrounding-occluded-windows",
    "--lang=en-US", "--no-first-run", "--no-default-browser-check", "about:blank",
], env={**os.environ, "DISPLAY": DISPLAY},
    stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
log(f"chromium pid {chromium.pid}")


# --------------------------------------------------------------------- VLM --
def llm_decide(screenshot_b64, round_no, last_state, iframe_text):
    """Ask the VLM to JUDGE the state. Tools carry no coordinates: aiming is
    the accessibility tree's job (or the fallback pixel guess, salvaged)."""
    if not API_KEY:
        raise RuntimeError("TURNSTILE_LLM_API_KEY (or MITTWALD_API_KEY) not set")
    prompt = (
        "You are supervising a real desktop browser loading a Cloudflare Turnstile "
        "'Verify you are human' challenge (site verify.spotbye.qzz.io). "
        "I give you a screenshot of the page PLUS the live text of the Turnstile "
        "iframe itself.\n\n"
        "Known states:\n"
        "- Countdown numbers, then a checkbox labelled 'Verify you are human' "
        "(left side of the widget box).\n"
        "- 'Checking your Browser...' or an EMPTY/spinner-only iframe means the "
        "page is being verified OR held by Cloudflare. While held, clicking does "
        "NOTHING and repeated clicking makes the session look automated - the "
        "only correct action is wait.\n"
        "- A spinning ring inside the checkbox = verification in flight: wait, "
        "do NOT click again.\n"
        "- Success: green checkmark, 'Success! Thank you!' text.\n\n"
        f"Decision round {round_no}. Last observation: {last_state or 'first look'}. "
        f"Iframe text now: {iframe_text or '(unreadable)'}. "
        "Choose exactly ONE action: click (a visible, idle checkbox - we will aim "
        "it precisely), wait (nothing actionable / verification in flight / held), "
        "or done (already solved)."
    )
    body = json.dumps({
        "model": LLM_MODEL,
        "messages": [
            {"role": "system", "content": "You supervise a browser via tools. Answer with exactly one tool call."},
            {"role": "user", "content": [
                {"type": "text", "text": prompt},
                {"type": "image_url", "image_url": {"url": f"data:image/png;base64,{screenshot_b64}"}},
            ]},
        ],
        "tools": [
            {"type": "function", "function": {
                "name": "click",
                "description": "Click the visible Turnstile checkbox (aiming is handled for you). Optional rough pixel hint (x,y) used only if the accessibility tree has no target.",
                "parameters": {"type": "object",
                                "properties": {"x": {"type": "integer"}, "y": {"type": "integer"}},
                                "required": []}}},
            {"type": "function", "function": {
                "name": "wait",
                "description": "Wait N seconds without clicking.",
                "parameters": {"type": "object",
                                "properties": {"seconds": {"type": "integer"}},
                                "required": ["seconds"]}}},
            {"type": "function", "function": {
                "name": "done",
                "description": "Stop: solved or permanently impossible.",
                "parameters": {"type": "object",
                                "properties": {"reason": {"type": "string"}},
                                "required": ["reason"]}}},
        ],
        "tool_choice": "auto",
        "extra_body": {"chat_template_kwargs": {"enable_thinking": False}},
    }).encode()
    req = urllib.request.Request(
        f"{LLM_BASE}/chat/completions", data=body,
        headers={"Authorization": f"Bearer {API_KEY}", "Content-Type": "application/json"})
    r = json.load(urllib.request.urlopen(req, timeout=120))
    msg = r["choices"][0]["message"]
    content = (msg.get("content") or "").strip()
    tcs = msg.get("tool_calls") or []
    if not tcs:
        return None, {}, content
    fn = tcs[0].get("function", {})
    raw_args = fn.get("arguments") or "{}"
    try:
        args = json.loads(raw_args)
    except json.JSONDecodeError:
        nums = re.findall(r"-?\d+", raw_args)
        args = {"x": int(nums[0]) if nums else None,
                "y": int(nums[1]) if len(nums) > 1 else None}
        log("tool args were not valid JSON, salvaged: %r (raw=%r)" % (args, raw_args[:120]))
    return fn.get("name"), args, content


def to_int(v):
    if v is None:
        return None
    if isinstance(v, (int, float)):
        return int(v)
    m = re.search(r"-?\d+", str(v))
    return int(m.group()) if m else None


# ------------------------------------------------------------ CDP helpers ---
STEALTH_JS = (
    "Object.defineProperty(navigator,'webdriver',{get:()=>undefined});"
    "window.chrome=window.chrome||{runtime:{} };"
    "Object.defineProperty(navigator,'plugins',{get:()=>[1,2,3,4,5]});"
    "Object.defineProperty(navigator,'languages',{get:()=>['en-US','en']});"
)


def find_cdp_target(port, kind, url_part=""):
    for _ in range(120):  # fresh-profile chromium can take >30s to come up
        try:
            r = json.load(urllib.request.urlopen(f"http://127.0.0.1:{port}/json/list", timeout=3))
            for t in r:
                if t.get("type") == kind and url_part in t.get("url", ""):
                    return t
        except Exception:
            pass
        time.sleep(0.5)
    return None


def ax_checkbox_local(iws):
    """Checkbox bounds in IFRAME-local pixels via the accessibility tree.
    Returns (x, y, w, h) or None. Mirrors solve12.py's proven plumbing."""
    tree = iws.cdp("Accessibility.getFullAXTree")
    for n in tree.get("nodes", []):
        role = n.get("role", {}).get("value", "")
        name = n.get("name", {}).get("value", "")
        if role.lower() != "checkbox" and "human" not in name.lower():
            continue
        b = n.get("bounds")
        if b:
            return b["x"], b["y"], b["width"], b["height"]
        bid = n.get("backendDOMNodeId")
        if bid:
            try:
                quads = iws.cdp("DOM.getContentQuads", {"backendNodeId": bid}).get("quads", [])
                if quads:
                    q = quads[0]  # [x1,y1,...,x4,y4]
                    xs, ys = q[0::2], q[1::2]
                    return min(xs), min(ys), max(xs) - min(xs), max(ys) - min(ys)
            except Exception as e:
                log("getContentQuads failed: %s" % str(e)[:100])
    return None


def frame_origin_page_px(ws):
    """Top-left of the Turnstile frame in PAGE pixels (owner box model).
    Page.getFrames was removed from recent chromium builds (-32601); any
    failure returns None and the caller falls back to VLM pixel hints."""
    try:
        frames = ws.cdp("Page.getFrames").get("frameTree", {})
    except Exception as e:
        log("Page.getFrames unavailable (%s) - VLM aiming only" % str(e)[:80])
        return None

    def find_cf(frame):
        if "challenges.cloudflare.com" in frame.get("url", ""):
            return frame
        for c in frame.get("childFrames", []):
            f = find_cf(c)
            if f:
                return f
        return None

    cf = find_cf(frames)
    if not cf:
        return None
    try:
        owner = ws.cdp("Page.getFrameOwner", {"frameId": cf["id"]})
        bm = ws.cdp("DOM.getBoxModel", {"nodeId": owner["nodeId"]})
        pts = bm["model"]["border"]
        return min(pts[0::2]), min(pts[1::2])
    except Exception as e:
        log("frame origin mapping failed (%s) - VLM aiming only" % str(e)[:80])
        return None


def human_click(ws, x, y):
    """Mouse dynamics shaped to read as human: eased bezier path from a
    random start, variable step cadence, dwell at target, short press."""
    sx, sy = random.randint(200, 900), random.randint(150, 500)
    # control point: perpendicular offset for a gentle arc
    mx, my = (sx + x) / 2, (sy + y) / 2
    dx, dy = x - sx, y - sy
    dist = math.hypot(dx, dy) or 1
    bend = random.uniform(-0.25, 0.25) * dist
    cx, cy = mx - dy / dist * bend, my + dx / dist * bend
    steps = random.randint(10, 16)
    for i in range(1, steps + 1):
        t = i / steps
        # ease-in-out so the motion accelerates then settles
        te = t * t * (3 - 2 * t)
        bx = (1 - te) ** 2 * sx + 2 * (1 - te) * te * cx + te ** 2 * x
        by = (1 - te) ** 2 * sy + 2 * (1 - te) * te * cy + te ** 2 * y
        ws.cdp("Input.dispatchMouseEvent", {"type": "mouseMoved", "x": int(bx), "y": int(by)})
        time.sleep(jrand(0.012, 0.045))
    # humans aim slightly off-centre
    tx, ty = x + random.randint(-4, 4), y + random.randint(-3, 3)
    time.sleep(jrand(0.25, 0.7))  # dwell before committing
    ws.cdp("Input.dispatchMouseEvent", {"type": "mousePressed", "x": tx, "y": ty,
                                        "button": "left", "buttons": 1, "clickCount": 1})
    time.sleep(jrand(0.07, 0.15))
    ws.cdp("Input.dispatchMouseEvent", {"type": "mouseReleased", "x": tx, "y": ty,
                                        "button": "left", "clickCount": 1})


def key_event(ws, etype, code, vk, text=None):
    p = {"type": etype, "code": code, "windowsVirtualKeyCode": vk}
    if text:
        p["text"] = text
    ws.cdp("Input.dispatchKeyEvent", p)


def keyboard_tab_space(ws):
    key_event(ws, "keyDown", "Tab", 9)
    time.sleep(0.05)
    key_event(ws, "keyUp", "Tab", 9)
    time.sleep(jrand(0.3, 0.6))
    key_event(ws, "keyDown", "Space", 32, text=" ")
    time.sleep(0.05)
    key_event(ws, "keyUp", "Space", 32)


# -------------------------------------------------------------------- main --
def main():
    page = find_cdp_target(DEBUG_PORT, "page")
    if not page:
        log("FATAL: no CDP page target")
        return 2
    ws = WsClient(page["webSocketDebuggerUrl"])
    send = ws.cdp

    cf_requests = []

    def on_event(ev):
        if ev.get("method") == "Network.requestWillBeSent":
            u = ev.get("params", {}).get("request", {}).get("url", "")
            if "challenges.cloudflare.com" in u or "spotbye" in u:
                cf_requests.append(u)

    ws.on_event = on_event

    def ev(expr):
        return send("Runtime.evaluate", {"expression": expr, "returnByValue": True}).get("result", {}).get("value")

    def observe():
        loc = ev("location.href") or ""
        tok = ev("(document.querySelector('input[name=cf-turnstile-response]')||{}).value||''") or ""
        status = ev("(document.getElementById('status')||{}).textContent||''") or ""
        shot = send("Page.captureScreenshot", {"format": "png"}).get("data", "")
        return loc, tok, status, shot

    send("Page.enable")
    send("Runtime.enable")
    send("DOM.enable")
    send("Network.enable")
    send("Page.addScriptToEvaluateOnNewDocument", {"source": STEALTH_JS})
    send("Page.navigate", {"url": CHALLENGE})
    log("navigated")

    # Turnstile OOPIF target (separate CDP endpoint) - may take a few seconds
    iframe = find_cdp_target(DEBUG_PORT, "iframe", "challenges.cloudflare.com")
    iws = None
    if iframe:
        iws = WsClient(iframe["webSocketDebuggerUrl"])
        iws.cdp("Accessibility.enable")
        iws.cdp("DOM.enable")
        iws.cdp("Runtime.enable")
        log("OOPIF target attached")
    else:
        log("WARN: no OOPIF target yet - falling back to VLM pixel hints only")

    def iframe_text():
        if not iws:
            return ""
        try:
            return iws.cdp("Runtime.evaluate",
                           {"expression": "document.title + ' ||| ' + (document.body ? document.body.innerText.slice(0,150) : '')",
                            "returnByValue": True}).get("result", {}).get("value", "")
        except Exception as e:
            return f"eval-err:{str(e)[:60]}"

    rc = 3
    last_state = None
    clicks = 0
    for rnd in range(1, MAX_ROUNDS + 1):
        time.sleep(jrand(5, 9))  # human-ish settle (widget has a ~5s countdown)
        loc, tok, status, shot = observe()
        if loc.startswith(CB.split("?")[0]) or tok:
            log(f"round {rnd}: ALREADY SOLVED (loc={loc[:60]!r} tok={len(tok)})")
            rc = 0
            break
        itext = iframe_text()
        last_state = f"status={status!r} iframe={itext[:80]!r}"
        log(f"round {rnd}: {last_state}; asking VLM ({LLM_MODEL})")
        try:
            tool, args, raw = llm_decide(shot, rnd, last_state, itext)
        except Exception as e:
            log(f"round {rnd}: VLM call failed: {str(e)[:200]}")
            time.sleep(jrand(5, 10))
            continue
        if tool is None:
            log(f"round {rnd}: no tool call, raw={raw[:160]!r}; waiting")
            time.sleep(jrand(6, 12))
            continue
        if tool == "wait":
            secs = to_int(args.get("seconds")) or 8
            secs = max(3, min(secs, 15))
            log(f"round {rnd}: VLM says wait {secs}s")
            time.sleep(secs)
            continue
        if tool == "done":
            log(f"round {rnd}: VLM done: {args.get('reason', '')[:160]}")
            break
        if tool != "click":
            log(f"round {rnd}: unknown tool {tool!r}; waiting")
            time.sleep(jrand(5, 10))
            continue

        # ---- aim: accessibility tree first, VLM pixel hint as fallback ----
        target = None
        if iws:
            box = ax_checkbox_local(iws)
            if box:
                ox, oy = frame_origin_page_px(ws)
                if ox is not None:
                    lx, ly, w, h = box
                    target = (int(ox + lx + w * random.uniform(0.35, 0.6)),
                              int(oy + ly + h * random.uniform(0.4, 0.6)))
                    log(f"round {rnd}: AX target local=({lx:.0f},{ly:.0f},{w:.0f}x{h:.0f}) "
                        f"frame@({ox:.0f},{oy:.0f}) -> page {target}")
                else:
                    log(f"round {rnd}: AX box found but frame origin unmapped")
            else:
                log(f"round {rnd}: no AX checkbox bounds (hold state?)")
        if target is None:
            hx, hy = to_int(args.get("x")), to_int(args.get("y"))
            if hx is None and hy is None:
                nums = re.findall(r"-?\d+", json.dumps(args))
                if len(nums) >= 2:
                    hx, hy = int(nums[0]), int(nums[1])
            if hx is not None and hy is not None and 0 <= hx < 1280 and 0 <= hy < 900:
                target = (hx, hy)
                log(f"round {rnd}: using VLM pixel hint {target}")
            else:
                log(f"round {rnd}: no aim available; keyboard fallback next")
                time.sleep(jrand(4, 8))
                continue

        log(f"round {rnd}: human click {target}")
        human_click(ws, *target)
        clicks += 1

        # ---- watch for completion up to 40s ----
        for _ in range(13):
            time.sleep(jrand(2.5, 4))
            loc, tok, status, _ = observe()
            if loc.startswith(CB.split("?")[0]):
                log(f"REDIRECT REACHED: {loc[:100]}")
                open("/tmp/final_url_llm.txt", "w").write(loc)
                ws.close()
                if iws:
                    iws.close()
                return 0
            if tok:
                log(f"TOKEN IN HIDDEN INPUT (len {len(tok)})")
                ws.close()
                if iws:
                    iws.close()
                return 0
        n_new = len(cf_requests)
        log(f"round {rnd}: post-click watch done; status={status!r} cf_requests_total={n_new}")
        if n_new > 0:
            log("  recent: " + "; ".join(cf_requests[-3:])[:200])
        elif clicks % 2 == 0:
            # no network reaction at all - try the keyboard route once in a while
            log("round %d: no CF network reaction; keyboard Tab+Space fallback" % rnd)
            keyboard_tab_space(ws)

        # challenge JWT TTL is ~5-10 min: re-mint + re-navigate if we stall
        if rnd % 5 == 0 and rnd < MAX_ROUNDS:
            log(f"round {rnd}: re-minting challenge (fresh TTL)")
            try:
                fresh = mint_challenge(CB)
                send("Page.navigate", {"url": fresh})
            except Exception as e:
                log(f"round {rnd}: re-mint failed: {str(e)[:120]}")
    ws.close()
    if iws:
        iws.close()
    return rc


try:
    rc = main()
except Exception:
    log("EXCEPTION:\n" + traceback.format_exc())
    rc = 2
finally:
    try:
        chromium.terminate()
    except Exception:
        pass

# ------------------------------------------------------- grant + exchange ---
time.sleep(1)
try:
    cap = open(GRANT_LOG).read().strip()
except FileNotFoundError:
    cap = ""
log("capture log: " + (cap[:300] or "(empty)"))

grant = None
for path in (GRANT_LOG, "/tmp/final_url_llm.txt"):
    try:
        data = open(path).read()
    except FileNotFoundError:
        continue
    m = re.search(r"[?&]grant=([A-Za-z0-9_\-]+)", data)
    if m:
        grant = m.group(1)
        log(f"grant from {path}: {grant[:40]}...")
        break

if not grant:
    log("NO GRANT - exiting (rc=%d)" % rc)
    sys.exit(rc)

payload = json.dumps({
    "grant": grant,
    "install_id": CLI_INSTALL_ID,
    "app_version": "unknown",
    "platform": "desktop",
}).encode()
req = urllib.request.Request(
    VERIFY_BASE + "/session/exchange", data=payload,
    headers={"Content-Type": "application/json", "User-Agent": UA})
try:
    d = json.load(urllib.request.urlopen(req, timeout=20))
except Exception as e:
    log("EXCHANGE FAILED: " + str(e)[:300])
    sys.exit(1)
sid, ssec, exp = d.get("session_id"), d.get("session_secret"), d.get("expires_at")
if not sid or not ssec or not exp:
    log("INCOMPLETE EXCHANGE: " + json.dumps(d)[:300])
    sys.exit(1)
log(f"session ok: id={sid[:16]}... expires_at={exp}")

record = {"install_id": CLI_INSTALL_ID, "session_id": sid,
          "session_secret": ssec, "expires_at": exp}
os.makedirs(os.path.dirname(CLI_STORE), exist_ok=True)
with open(CLI_STORE, "w") as f:
    json.dump(record, f, indent=2)
pyrec = dict(record)
pyrec["refresh_after"] = d.get("refresh_after", "")
pyrec["capabilities"] = d.get("capabilities", [])
os.makedirs(os.path.dirname(PY_STORE), exist_ok=True)
with open(PY_STORE, "w") as f:
    json.dump(pyrec, f, indent=2)
log("WROTE both session stores - renewal complete")
sys.exit(0)
