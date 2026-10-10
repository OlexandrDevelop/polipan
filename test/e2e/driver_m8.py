# SPDX-License-Identifier: Apache-2.0
"""End-to-end driver of milestone M8 (run-m8.sh): T-E2E-04, T-E2E-09 alert part, T-E2E-15, AC-OBS-01, 03, 04 to 07,
AC-ALERT-01, 04, 05, AC-DEPLOY-03, REQ-OBS-102, REQ-OBS-104.

It runs on the host. The node is a Docker-in-Docker container. The panel is reached through the Caddy of the node
(Host: panel.test). The state of the node is read with `docker exec`.
Usage: driver_m8.py STEP NODE_CONTAINER NODE_IP SETUP_URL_FILE   (STEP: setup | canary | logs | search | alerts)
"""
import http.client
import json
import re
import subprocess
import sys
import threading
import time

PANEL = "panel.test"
PASSWORD = "correct horse battery"
EMAIL = "owner@example.com"
STATE_FILE = "/tmp/pp-m8-state.json"
RECEIVER = "http://192.168.78.10:8081"

STEP, N1, IP, SETUP_URL = (sys.argv + ["", "", "", ""])[1:5]


def fail(msg):
    print("FAIL:", msg, flush=True)
    sys.exit(1)


def ok(msg):
    print("ok:", msg, flush=True)


def run(*cmd, check=True, timeout=300, stdin=None):
    r = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout, input=stdin)
    if check and r.returncode != 0:
        fail(f"{' '.join(cmd)}: {r.stderr.strip() or r.stdout.strip()}")
    return r.stdout.strip() if r.returncode == 0 else ""


def dx(*cmd, check=True, timeout=300):
    return run("docker", "exec", N1, *cmd, check=check, timeout=timeout)


def wait(what, fn, timeout=120, every=1.5):
    end = time.time() + timeout
    last = None
    while time.time() < end:
        try:
            last = fn()
        except (OSError, http.client.HTTPException) as e:
            last = e
        if last and not isinstance(last, Exception):
            return last
        time.sleep(every)
    fail(f"timeout: {what} (last: {last})")


class Panel:
    def __init__(self):
        self.cookie, self.csrf = None, None

    def call(self, method, path, body=None, expect=None):
        headers = {"Host": PANEL, "Content-Type": "application/json"}
        if self.cookie:
            headers["Cookie"] = self.cookie
        if self.csrf and method != "GET":
            headers["X-CSRF-Token"] = self.csrf
        for attempt in range(6):  # Caddy can reset a connection while it loads its config
            try:
                conn = http.client.HTTPConnection(IP, 80, timeout=60)
                conn.request(method, "/api/v1" + path, json.dumps(body) if body is not None else None, headers)
                resp = conn.getresponse()
                raw = resp.read()
                break
            except (ConnectionError, TimeoutError, OSError):
                if attempt == 5:
                    raise
                time.sleep(1)
        for k, v in resp.getheaders():
            if k.lower() == "set-cookie" and v.startswith("pp_session="):
                self.cookie = v.split(";")[0]
            if k.lower() == "x-csrf-token":
                self.csrf = v
        data = json.loads(raw) if raw else None
        if expect is not None and resp.status != expect:
            fail(f"{method} {path}: want {expect}, got {resp.status}: {raw[:500]!r}")
        return resp.status, data

    def login(self):
        self.call("POST", "/auth/login", {"email": EMAIL, "password": PASSWORD}, expect=200)
        self.call("GET", "/me", expect=200)


def http_get(host, path="/", timeout=5):
    try:
        conn = http.client.HTTPConnection(IP, 80, timeout=timeout)
        conn.request("GET", path, headers={"Host": host})
        r = conn.getresponse()
        return r.status, r.read().decode("latin1").strip()
    except (OSError, http.client.HTTPException):
        return 0, ""


def load_state():
    try:
        return json.load(open(STATE_FILE))
    except FileNotFoundError:
        return {}


def save_state(**kw):
    st = load_state()
    st.update(kw)
    json.dump(st, open(STATE_FILE, "w"))


def logged_in():
    p = Panel()
    p.login()
    return p


def server_up():
    r = subprocess.run(["docker", "exec", N1, "docker", "exec", "server", "/polipan-server", "healthcheck"], capture_output=True, timeout=30)
    return r.returncode == 0


def receiver_messages():
    return json.loads(dx("wget", "-qO-", RECEIVER + "/messages"))


def set_vars(p, project_id, svc_id, values):
    _, existing = p.call("GET", f"/projects/{project_id}/variables?service_id={svc_id}", expect=200)
    by_name = {v["name"]: v for v in existing}
    for name, value in values.items():
        if name in by_name:
            p.call("PATCH", f"/variables/{by_name[name]['id']}", {"name": name, "value": value, "service_id": svc_id}, expect=200)
        else:
            p.call("POST", f"/projects/{project_id}/variables", {"name": name, "value": value, "service_id": svc_id}, expect=201)


def dep_wait(p, dep_id, timeout=400, states=("live", "failed", "rolled_back", "cancelled")):
    def done():
        _, d = p.call("GET", f"/deployments/{dep_id}", expect=200)
        return d if d["state"] in states else None
    return wait(f"deployment {dep_id} ends", done, timeout=timeout, every=1)


def deploy(p, svc_id, body=None, expect="live", timeout=400):
    _, dep = p.call("POST", f"/services/{svc_id}/deploy", body or {}, expect=202)
    d = dep_wait(p, dep["id"], timeout)
    if d["state"] != expect:
        fail(f"deployment {d['id']} is {d['state']} ({d.get('failure_reason')}: {d.get('failure_detail')}), want {expect}")
    return d


# The app of the tests. VERSION is in the answer. BAD=1 gives 500 for every page but /ready. FAIL=1 makes /ready 503.
APP = r"""
import os, sys, threading, time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
V = os.environ.get("VERSION", "0")
BAD = os.environ.get("BAD") == "1"
FAIL = os.environ.get("FAIL") == "1"
LOGGER = os.environ.get("LOGGER") == "1"
class H(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    def log_message(self, *a): pass
    def reply(self, code, body):
        b = body.encode()
        self.send_response(code); self.send_header("Content-Length", str(len(b))); self.end_headers(); self.wfile.write(b)
    def do_GET(self):
        if self.path.split("?")[0] == "/ready":
            return self.reply(503, "not ready yet") if FAIL else self.reply(200, "ready")
        time.sleep(0.01)
        if BAD: return self.reply(500, "bad")
        self.reply(200, "version " + V)
def numbers():
    n = 0
    while True:
        n += 1
        print("seq %d t=%.3f" % (n, time.time()), flush=True)
        time.sleep(0.05)
if LOGGER: threading.Thread(target=numbers, daemon=True).start()
ThreadingHTTPServer(("0.0.0.0", 8000), H).serve_forever()
"""


def create_app(p, env_id, name, domain, variables, project_id):
    cfg = {"image": "python:3.12-slim", "port": 8000, "command": ["python", "-u", "-c", APP],
           "health": {"type": "http", "path": "/ready", "interval": 1, "timeout": 2, "healthy_threshold": 2, "unhealthy_threshold": 3,
                      "start_period": 3, "deploy_timeout": 40},
           "strategy_options": {"drain_seconds": 3}}
    _, svc = p.call("POST", f"/environments/{env_id}/services", {"name": name, "type": "app", "source": {"image": cfg["image"]}, "config": cfg}, expect=201)
    if domain:
        p.call("POST", f"/services/{svc['id']}/domains", {"host": domain, "container_port": 8000, "https": False}, expect=201)
    set_vars(p, project_id, svc["id"], variables)
    return svc


class Load:
    """Sends `rate` requests per second to a host through Caddy. Every request has its own connection."""

    def __init__(self, host, rate=30):
        self.host, self.rate, self.log, self.lock = host, rate, [], threading.Lock()
        self.stop_flag = threading.Event()
        self.thread = threading.Thread(target=self.run, daemon=True)

    def one(self):
        t0 = time.time()
        try:
            conn = http.client.HTTPConnection(IP, 80, timeout=10)
            conn.request("GET", "/", headers={"Host": self.host, "Connection": "close"})
            r = conn.getresponse()
            rec = (t0, r.status, r.read().decode("latin1"))
            conn.close()
        except Exception as e:  # a failed request is a result
            rec = (t0, 0, repr(e))
        with self.lock:
            self.log.append(rec)

    def run(self):
        nxt = time.time()
        while not self.stop_flag.is_set():
            threading.Thread(target=self.one, daemon=True).start()
            nxt += 1.0 / self.rate
            time.sleep(max(0, nxt - time.time()))

    def start(self):
        self.thread.start()
        return self

    def stop(self):
        self.stop_flag.set()
        self.thread.join()
        time.sleep(1.5)
        return list(self.log)

    def between(self, t0, t1):
        with self.lock:
            return [r for r in self.log if t0 <= r[0] < t1]


def share(rows, version):
    good = [r for r in rows if r[1] == 200]
    return 100.0 * sum(1 for r in good if r[2] == f"version {version}") / max(1, len(rows))


# ---- steps ----

def setup():
    p = Panel()
    wait("the panel answers through Caddy", lambda: http_get(PANEL, "/healthz")[0] == 200, timeout=240)
    token = re.search(r"token=([0-9a-f]+)", open(SETUP_URL).read()).group(1)
    p.call("POST", "/setup/first-user", {"setup_token": token, "email": EMAIL, "name": "Owner", "password": PASSWORD}, expect=201)
    p.login()
    _, nodes = p.call("GET", "/nodes", expect=200)
    wait("the node is ready", lambda: [n for n in p.call("GET", "/nodes")[1] if n["state"] == "ready"], timeout=120)
    node = p.call("GET", "/nodes")[1][0]
    _, org = p.call("GET", "/org", expect=200)
    # The fake receiver is on a private network: the org allows private targets (REQ-SEC-104).
    p.call("PATCH", "/org", {"settings": {"allow_private_targets": True}}, expect=200)
    _, proj = p.call("POST", "/projects", {"name": "shop"}, expect=201)
    _, envs = p.call("GET", f"/projects/{proj['id']}/environments", expect=200)
    prod = next(e for e in envs if e["kind"] == "production")
    stg = next(e for e in envs if e["kind"] != "production")
    # Channels (REQ-ALERT-001). The first one is set on the default rules.
    _, tg = p.call("POST", "/alert-channels", {"kind": "telegram", "name": "telegram", "config": {"chat_id": "7"}, "secret": "123:e2e-bot-token"}, expect=201)
    _, sl = p.call("POST", "/alert-channels", {"kind": "slack", "name": "slack", "secret": RECEIVER + "/slack"}, expect=201)
    _, wh = p.call("POST", "/alert-channels", {"kind": "webhook", "name": "hook", "config": {"url": RECEIVER + "/hook"}, "secret": "hook-secret"}, expect=201)
    for ch in (tg, sl, wh):
        _, res = p.call("POST", f"/alert-channels/{ch['id']}/test", expect=200)
        if not res.get("ok"):
            fail(f"test of channel {ch['name']}: {res}")
    wait("three test messages", lambda: len(receiver_messages()) >= 3, timeout=30, every=1)
    kinds = sorted(m["kind"] for m in receiver_messages())
    if kinds != ["slack", "telegram", "webhook"]:
        fail(f"test messages: {kinds}")
    msgs = receiver_messages()
    if any("e2e-bot-token" in m["body"] for m in msgs if m["kind"] != "telegram") or not any(m["signature"].startswith("sha256=") for m in msgs if m["kind"] == "webhook"):
        fail("a secret leaked or the webhook is not signed")
    # One rule that sends the end of a rollback to all channels (AC-ALERT-04).
    p.call("POST", "/alert-rules", {"name": "rollbacks", "type": "deployment_rolled_back", "severity": "warning",
                                    "channel_ids": [tg["id"], sl["id"], wh["id"]]}, expect=201)
    save_state(org=org["id"], project=proj["id"], prod=prod["id"], stg=stg["id"], node=node["id"], telegram=tg["id"])
    ok("setup: the node is ready, three channels sent a test message, the secrets stayed inside, the webhook is signed")


def canary():
    p = logged_in()
    st = load_state()
    # T-E2E-04, AC-DEPLOY-03.
    svc = create_app(p, st["prod"], "shop", "shop.test", {"VERSION": "1", "BAD": "0"}, st["project"])
    deploy(p, svc["id"])
    wait("v1 serves", lambda: http_get("shop.test")[1] == "version 1", timeout=60)
    save_state(shop=svc["id"])
    opts = {"strategy": "canary", "strategy_options": {"canary_steps": [10, 25, 50, 100], "canary_step_interval_seconds": 60}}

    ld = Load("shop.test").start()
    time.sleep(3)
    set_vars(p, st["project"], svc["id"], {"VERSION": "2"})
    _, dep = p.call("POST", f"/services/{svc['id']}/deploy", opts, expect=202)
    if dep["strategy"] != "canary":
        fail(f"strategy {dep['strategy']}")
    seen, windows = [], {}  # canary_step -> (first time seen, last time seen)
    while True:
        _, d = p.call("GET", f"/deployments/{dep['id']}", expect=200)
        now = time.time()
        if d.get("canary_step") and d["state"] == "shifting":
            a, _b = windows.get(d["canary_step"], (now, now))
            windows[d["canary_step"]] = (a, now)
            if d["canary_step"] not in seen:
                seen.append(d["canary_step"])
        if d["state"] in ("live", "failed", "rolled_back", "cancelled"):
            break
        if now - ld.log[0][0] > 700:
            fail("the canary takes too long")
        time.sleep(1)
    log = ld.stop()
    if d["state"] != "live":
        fail(f"the good canary ended {d['state']} ({d.get('failure_reason')}: {d.get('failure_detail')})")
    if seen != [10, 25, 50]:
        fail(f"steps seen: {seen}")
    for step, (a, b) in windows.items():
        rows = ld.between(a + 10, b)  # the first seconds are the change of the route
        s = share(rows, 2)
        print(f"  step {step}%: {len(rows)} requests, {s:.1f}% went to the new version", flush=True)
        if abs(s - step) > 15:
            fail(f"step {step}: {s:.1f}% of the requests went to the new version")
    bad = [r for r in log if r[1] != 200]
    if bad:
        fail(f"{len(bad)} of {len(log)} requests failed in the good canary, first: {bad[0]}")
    ok("T-E2E-04: a good version went 10, 25, 50, 100, the shares of the traffic match, no request failed")
    if http_get("shop.test")[1] != "version 2":
        fail("the new version does not serve after 100%")
    gate = d.get("canary_gate") or {}
    if not gate.get("passed") or not gate.get("health_ok"):
        fail(f"the last gate: {gate}")

    # A bad version: its 5xx rate trips the gate at step 1. The old version serves again.
    ld = Load("shop.test").start()
    time.sleep(3)
    set_vars(p, st["project"], svc["id"], {"VERSION": "3", "BAD": "1"})
    _, dep = p.call("POST", f"/services/{svc['id']}/deploy", opts, expect=202)
    d = dep_wait(p, dep["id"], timeout=400)
    t_end = time.time()
    time.sleep(8)
    log = ld.stop()
    if d["state"] != "rolled_back" or d["failure_reason"] != "gate_5xx":
        fail(f"the bad canary ended {d['state']} ({d.get('failure_reason')}: {d.get('failure_detail')})")
    if d.get("canary_step") != 10:
        fail(f"the gate tripped at step {d.get('canary_step')}, want 10")
    after = [r for r in log if r[0] > t_end + 2]
    if not after or any(r[1] != 200 or r[2] != "version 2" for r in after):
        fail("after the rollback not all requests got version 2 from the old containers")
    errors = sum(1 for r in log if r[1] == 500)
    ok(f"AC-DEPLOY-03: the bad version rolled back by itself at step 1 with gate_5xx ({errors} of {len(log)} requests got a 500): {d['failure_detail']}")
    _, s = p.call("GET", f"/services/{svc['id']}", expect=200)
    if s.get("live_release_id") is None or any(x["role"] == "live" and x.get("release_id") == dep.get("release_id") for x in s.get("placements", [])):
        fail("the new release is still live")
    # AC-ALERT-04: the rollback has a message with the reason code in the three channels.
    def alerted():
        m = [x for x in receiver_messages() if "gate_5xx" in x["body"]]
        return m if {x["kind"] for x in m} == {"slack", "telegram", "webhook"} else None
    wait("the rollback alert in three channels", alerted, timeout=60, every=2)
    ok("AC-ALERT-04: the rollback message with gate_5xx reached Telegram, Slack and the webhook")
    set_vars(p, st["project"], svc["id"], {"VERSION": "2", "BAD": "0"})


def logs():
    p = logged_in()
    st = load_state()
    _, old = p.call("GET", f"/environments/{st['prod']}/services", expect=200)
    for o in old if isinstance(old, list) else old.get("items", []):
        if o["name"] == "logger":  # a repeated run
            p.call("DELETE", f"/services/{o['id']}?confirm=logger", expect=204)
    svc = create_app(p, st["prod"], "logger", None, {"VERSION": "1", "LOGGER": "1"}, st["project"])
    deploy(p, svc["id"])
    save_state(logger=svc["id"])

    def newest():
        _, rows = p.call("GET", f"/services/{svc['id']}/logs?limit=5", expect=200)
        return [r for r in rows if "seq" in r.get("message", "")]
    wait("log lines of the service", newest, timeout=60, every=1)
    # AC-OBS-01: a line is in the store about 3 s after it is written.
    lags = []
    for _ in range(10):
        rows = newest()
        t = float(rows[-1]["message"].split("t=")[1])
        lags.append(time.time() - t)
        time.sleep(1)
    print(f"  lag of the newest line: min {min(lags):.1f}, median {sorted(lags)[5]:.1f}, max {max(lags):.1f} s", flush=True)
    if sorted(lags)[5] > 3.0:
        fail(f"AC-OBS-01: the median lag is {sorted(lags)[5]:.1f} s")
    ok("AC-OBS-01: new lines appear within 3 s")
    rows = newest()
    if not all(r.get("node_id") == st["node"] for r in rows):
        fail(f"the lines have no node label: {rows[:1]}")
    ok("AC-OBS-02 (one node): every line has the node label")

    # REQ-OBS-102 and AC-OBS-04: the server is away 50 s, the agent is killed in that time. Nothing is lost or repeated.
    run("docker", "exec", N1, "docker", "stop", "-t", "5", "server")
    time.sleep(20)
    run("docker", "exec", N1, "docker", "kill", "agent")  # SIGKILL: no clean stop. systemd starts it again, so does the test
    time.sleep(2)
    run("docker", "exec", N1, "docker", "start", "agent")
    time.sleep(30)
    run("docker", "exec", N1, "docker", "start", "server")
    wait("the server is back", server_up, timeout=120, every=2)
    p = Panel()
    wait("login after the restart", lambda: (p.login() or True), timeout=120, every=3)
    time.sleep(40)  # the spool is sent
    p.call("POST", f"/services/{svc['id']}/stop", expect=202)
    # The app is PID 1 of its container and ignores SIGTERM, so Docker waits for the grace time before it kills it.
    wait("the logger container has stopped", lambda: not dx("docker", "ps", "--filter", f"label=polipan.service={svc['id']}", "--format", "{{.Names}}"), timeout=120, every=2)
    time.sleep(20)  # the last lines are shipped

    def container_seqs():
        name = dx("docker", "ps", "-a", "--filter", f"label=polipan.service={svc['id']}", "--format", "{{.Names}}").split()[0]
        text = run("docker", "exec", N1, "docker", "logs", name, timeout=120)
        return [int(m.group(1)) for m in re.finditer(r"^seq (\d+) ", text, re.M)]
    want = container_seqs()

    def stored_seqs():
        out, cursor = [], ""
        while True:
            q = f"/logs/search?service_id={svc['id']}&q=seq&limit=200" + (f"&cursor={cursor}" if cursor else "")
            _, page = p.call("GET", q, expect=200)
            out += [int(m.group(1)) for r in page["items"] for m in [re.match(r"seq (\d+) ", r["message"])] if m]
            cursor = page.get("next_cursor")
            if not cursor:
                return out
    got = stored_seqs()
    dup = len(got) - len(set(got))
    missing = sorted(set(want) - set(got))
    extra = sorted(set(got) - set(want))
    print(f"  lines: {len(want)} written, {len(got)} in the store, {dup} repeated, {len(missing)} missing", flush=True)
    if dup or missing or extra:
        fail(f"REQ-OBS-102: repeated {dup}, missing {missing[:10]}, extra {extra[:10]}")
    ok("AC-OBS-04, REQ-OBS-102: the logs of the 50 s gap and of the agent kill are all there, none twice")
    p.call("POST", f"/services/{svc['id']}/start", expect=202)


def search():
    p = logged_in()
    st = load_state()
    _, existing = p.call("GET", f"/environments/{st['stg']}/services", expect=200)
    existing = existing if isinstance(existing, list) else existing.get("items", [])
    stg_svc = next((x for x in existing if x["name"] == "stage"), None)
    if stg_svc is None:
        _, stg_svc = p.call("POST", f"/environments/{st['stg']}/services", {"name": "stage", "type": "app", "source": {"image": "python:3.12-slim"},
                                                                          "config": {"image": "python:3.12-slim", "port": 8000}}, expect=201)
    _, seeded = p.call("GET", "/logs/search?q=%22calling%20upstream%203%22&limit=1", expect=200)
    if not seeded["items"]:  # a repeated run does not seed again
        run("docker", "exec", N1, "docker", "run", "--rm", "--network", "cp", "-v", "/e2e:/e2e", "python:3.12-slim", "python", "/e2e/seed.py",
            st["org"], st["project"], st["prod"], st["shop"], st["stg"], stg_svc["id"], st["node"], timeout=600)
        wait("the seeded lines can be searched", lambda: p.call("GET", "/logs/search?q=%22calling%20upstream%203%22&limit=1")[1]["items"], timeout=60, every=2)
    # AC-OBS-06: "timeout" with the production filter: only production lines, newest first.
    _, page = p.call("GET", f"/logs/search?q=timeout&environment_id={st['prod']}&limit=50", expect=200)
    items = page["items"]
    if len(items) != 6 or any(r["environment_id"] != st["prod"] for r in items) or any("timeout" not in r["message"] for r in items):
        fail(f"search: {[(r['message'], r['environment_id']) for r in items]}")
    times = [r["time"] for r in items]
    if times != sorted(times, reverse=True):
        fail(f"not newest first: {times}")
    ok("AC-OBS-06: the search for timeout in production gives the 6 production lines, newest first")
    _, page = p.call("GET", f"/logs/search?q=timeout&environment_id={st['stg']}", expect=200)
    if len(page["items"]) != 5:
        fail(f"staging: {len(page['items'])}")
    # A phrase and a regex.
    _, page = p.call("GET", "/logs/search?q=" + "%22calling%20upstream%203%22", expect=200)
    if len(page["items"]) != 1:
        fail(f"phrase: {len(page['items'])}")
    _, page = p.call("GET", "/logs/search?q=" + "%2Frequest%20%5B0-2%5D%20ok%2F", expect=200)
    if len(page["items"]) != 3:
        fail(f"regex: {len(page['items'])}")
    ok("the phrase and the regex search work")

    # AC-OBS-05: the range of 60 days. The query step keeps the point count under 1500.
    now = time.time()
    frm = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(now - 60 * 86400))
    to = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(now))
    _, res = p.call("GET", f"/services/{st['shop']}/metrics?metrics=cpu&from={frm}&to={to}", expect=200)
    pts = res["series"][0]["points"] if res["series"] else []
    if not pts or len(pts) > 1500 or res["step_seconds"] < 15:
        fail(f"60 days: {len(pts)} points, step {res.get('step_seconds')}")
    if pts[0][0] > now - 59 * 86400:
        fail(f"the range starts too late: {pts[0][0]}")
    ok(f"AC-OBS-05: 60 days of data give {len(pts)} points with a step of {res['step_seconds']} s (at most 1500)")
    # The labels of the seeded series belong to the org and the project (the tenant labels of REQ-OBS-100).
    labels = run("docker", "exec", N1, "docker", "run", "--rm", "--network", "cp", "python:3.12-slim", "python", "-c",
                 "import base64,urllib.request as u;r=u.Request('http://vm:8428/api/v1/series?match[]=polipan_container_cpu_usage_millis',headers={'Authorization':'Basic '+base64.b64encode(b'polipan:vpw').decode()});print(u.urlopen(r).read().decode())")
    data = json.loads(labels)["data"]
    if not data or any(s.get("org") != st["org"] or s.get("project") != st["project"] for s in data if s.get("service") == st["shop"]):
        fail(f"labels: {data[:2]}")
    # Metrics the agent sent itself carry the labels of its services.
    real = [s for s in data if s.get("release") != "rel_seed" and s.get("service")]
    if not real or any(s["org"] != st["org"] or s["project"] != st["project"] or s["environment"] not in (st["prod"], st["stg"]) for s in real):
        fail(f"labels of the real series: {real[:2]}")
    ok(f"the {len(real)} real container series have the org, project and environment of their service")

    # AC-OBS-07: logs older than the retention of the org are not returned.
    _, page = p.call("GET", f"/logs/search?q=%22old%20days%22&environment_id={st['prod']}", expect=200)
    if len(page["items"]) != 1:
        fail(f"before: the old line is not found ({len(page['items'])})")
    code, body = p.call("PATCH", "/org", {"settings": {"logs_retention_days": 30}})
    if code != 409:
        fail(f"a shorter retention needs a confirm: {code} {body}")
    p.call("PATCH", "/org", {"settings": {"logs_retention_days": 30, "confirm_retention_shrink": True}}, expect=200)
    _, page = p.call("GET", f"/logs/search?q=%22old%20days%22&environment_id={st['prod']}", expect=200)
    _, page2 = p.call("GET", f"/logs/search?q=%22old%20days%22&from=2020-01-01T00:00:00Z", expect=200)
    if page["items"] or page2["items"]:
        fail("AC-OBS-07: a line older than the retention was returned")
    code, _ = p.call("PATCH", "/org", {"settings": {"logs_retention_days": 400}})
    if code != 422:
        fail(f"a retention above the store limit: {code}")
    ok("AC-OBS-07: with a retention of 30 days the line of 45 days ago is not returned, also not with an old from")
    p.call("PATCH", "/org", {"settings": {"logs_retention_days": 90, "confirm_retention_shrink": True}}, expect=200)


def alerts():
    p = logged_in()
    st = load_state()
    base = len(receiver_messages())
    # AC-ALERT-04: a failed deployment gives a message with the reason code.
    set_vars(p, st["project"], st["shop"], {"VERSION": "9", "BAD": "0", "FAIL": "1"})
    _, dep = p.call("POST", f"/services/{st['shop']}/deploy", {}, expect=202)
    d = dep_wait(p, dep["id"], timeout=200)
    if d["state"] != "failed" or d["failure_reason"] != "health_timeout":
        fail(f"the deployment ended {d['state']} {d.get('failure_reason')}")
    wait("the failed deployment message", lambda: [m for m in receiver_messages()[base:] if "health_timeout" in m["body"] and m["kind"] == "telegram"], timeout=60, every=2)
    ok("AC-ALERT-04: the failed deployment reached Telegram with health_timeout")
    set_vars(p, st["project"], st["shop"], {"FAIL": "0", "VERSION": "2"})

    # AC-ALERT-01 and REQ-OBS-104: the node stops its heartbeat. One message. A server restart sends nothing again.
    base = len(receiver_messages())
    run("docker", "exec", N1, "docker", "stop", "-t", "2", "agent")
    wait("the node down message", lambda: [m for m in receiver_messages()[base:] if m["kind"] == "telegram" and "down" in m["body"].lower()], timeout=120, every=3)
    time.sleep(45)  # more than a repeat of the evaluation and several engine passes
    downs = [m for m in receiver_messages()[base:] if m["kind"] == "telegram" and "down" in m["body"].lower()]
    if len(downs) != 1:
        fail(f"AC-ALERT-01: {len(downs)} node down messages")
    ok("AC-ALERT-01: one Telegram message for the node that stopped, and none more while it stays down")
    run("docker", "exec", N1, "docker", "kill", "server")
    time.sleep(2)
    run("docker", "exec", N1, "docker", "start", "server")
    wait("the server is back", server_up, timeout=120, every=2)
    time.sleep(40)
    downs = [m for m in receiver_messages()[base:] if m["kind"] == "telegram" and "down" in m["body"].lower()]
    if len(downs) != 1:
        fail(f"REQ-OBS-104: after the server restart there are {len(downs)} node down messages")
    ok("REQ-OBS-104: the server restart did not send the firing alert again")
    run("docker", "exec", N1, "docker", "start", "agent")
    wait("the resolved message", lambda: [m for m in receiver_messages()[base:] if m["kind"] == "telegram" and "resolved" in m["body"].lower()], timeout=180, every=3)
    ok("AC-ALERT-01: the resolved message arrived when the node returned")

    # AC-ALERT-05 (live): a channel that answers 500 is marked failing and shows a warning. It recovers.
    p = logged_in()
    dx("wget", "-qO-", "--post-data=", RECEIVER + "/fail?on=1")
    _, res = p.call("POST", f"/alert-channels/{st['telegram']}/test", expect=200)
    _, chans = p.call("GET", "/alert-channels", expect=200)
    ch = next(c for c in chans if c["id"] == st["telegram"])
    if res.get("ok") or ch["state"] != "failing" or "500" not in ch.get("last_error", ""):
        fail(f"AC-ALERT-05: {res} {ch}")
    dx("wget", "-qO-", "--post-data=", RECEIVER + "/fail?on=0")
    _, res = p.call("POST", f"/alert-channels/{st['telegram']}/test", expect=200)
    _, chans = p.call("GET", "/alert-channels", expect=200)
    if not res.get("ok") or next(c for c in chans if c["id"] == st["telegram"])["state"] != "ok":
        fail("the channel did not recover")
    ok("AC-ALERT-05: a channel with a 500 is shown as failing with the error, and recovers")


if __name__ == "__main__":
    {"setup": setup, "canary": canary, "logs": logs, "search": search, "alerts": alerts}[STEP]()
