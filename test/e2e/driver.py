# SPDX-License-Identifier: Apache-2.0
"""End-to-end driver (T-E2E-01, T-E2E-02, T-E2E-03, T-E2E-13, AC-AUTH-03, AC-DOM-01..05, AC-APP-03, AC-APP-05,
AC-DEPLOY-01, 02, 09, AC-REL-01, 02, 07, AC-HC-01, 03, 04, 05 and the chaos kill of REQ-DEPLOY-107).

It runs inside the test Docker host with host networking. The panel is reached through the
Caddy of the node (Host: panel.test), so the panel route is tested too.
Usage: driver.py flow | gitflow | rollback | zerodowntime | chaos-start | chaos-end | serving | after
M4 (run-m4.sh): m4-setup | m4-backup | m4-restore | m4-check-corrupt | m4-volume | m4-controlplane | m4-template
"""
import http.client
import json
import re
import ssl
import sys
import time
import urllib.request

PANEL = "panel.test"
APP = "app.test"
BAD = "bad.test"
PASSWORD = "correct horse battery"
EMAIL = "owner@example.com"
STATE = "/e2e/state.json"


def fail(msg):
    print("FAIL:", msg)
    sys.exit(1)


def ok(msg):
    print("ok:", msg, flush=True)


class Panel:
    def __init__(self):
        self.cookie = None
        self.csrf = None

    def call(self, method, path, body=None, expect=None):
        headers = {"Host": PANEL, "Content-Type": "application/json"}
        if self.cookie:
            headers["Cookie"] = self.cookie
        if self.csrf and method != "GET":
            headers["X-CSRF-Token"] = self.csrf
        for attempt in range(6):  # Caddy can reset a connection while it reloads its config
            try:
                conn = http.client.HTTPConnection("127.0.0.1", 80, timeout=30)
                conn.request(method, "/api/v1" + path, json.dumps(body) if body is not None else None, headers)
                resp = conn.getresponse()
                raw = resp.read()
                break
            except ConnectionError:
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
            fail(f"{method} {path}: want {expect}, got {resp.status}: {raw[:300]!r}")
        return resp.status, data

    def login(self):
        self.call("POST", "/auth/login", {"email": EMAIL, "password": PASSWORD}, expect=200)
        self.call("GET", "/me", expect=200)


def wait(what, fn, timeout=120, every=2):
    end = time.time() + timeout
    last = None
    while time.time() < end:
        try:
            last = fn()
        except OSError as e:  # the connection can fail while Caddy loads its config
            last = e
        if last:
            return last
        time.sleep(every)
    fail(f"timeout: {what} (last: {last})")


def dns_add(host):
    req = urllib.request.Request("http://127.0.0.1:8055/add-a", json.dumps({"host": host, "addresses": ["172.30.0.1"]}).encode(), method="POST")
    urllib.request.urlopen(req, timeout=10).read()


def trust_context():
    # Pebble gives a new root for every start: ask it.
    insecure = ssl.create_default_context()
    insecure.check_hostname = False
    insecure.verify_mode = ssl.CERT_NONE
    pem = urllib.request.urlopen("https://127.0.0.1:15000/roots/0", context=insecure, timeout=10).read()
    ctx = ssl.create_default_context(cadata=pem.decode())
    return ctx


def https_get(host, ctx):
    """GET https://host/ with a certificate check against the Pebble root. Returns (status, body)."""
    import socket
    sock = socket.create_connection(("127.0.0.1", 443), timeout=15)
    tls = ctx.wrap_socket(sock, server_hostname=host)
    tls.sendall(f"GET / HTTP/1.1\r\nHost: {host}\r\nConnection: close\r\n\r\n".encode())
    data = b""
    while True:
        chunk = tls.recv(65536)
        if not chunk:
            break
        data += chunk
    tls.close()
    status = int(data.split(b" ", 2)[1])
    return status, data.decode("latin1")


def http_get(host, path="/"):
    conn = http.client.HTTPConnection("127.0.0.1", 80, timeout=15)
    conn.request("GET", path, headers={"Host": host})
    r = conn.getresponse()
    return r.status, r.getheader("Location"), r.read().decode("latin1")


def flow():
    p = Panel()
    wait("panel behind caddy", lambda: http_get(PANEL, "/healthz")[0] == 200, timeout=180)
    ok("panel answers through Caddy")

    # AC-AUTH-03: first user, setup URL works once.
    token = re.search(r"token=([0-9a-f]+)", open("/e2e/setup-url").read()).group(1)
    s, _ = p.call("POST", "/setup/first-user", {"setup_token": "wrong", "email": EMAIL, "name": "Owner", "password": PASSWORD})
    if s == 201:
        fail("setup with a wrong token must not work")
    p.call("POST", "/setup/first-user", {"setup_token": token, "email": EMAIL, "name": "Owner", "password": PASSWORD}, expect=201)
    p.call("POST", "/setup/first-user", {"setup_token": token, "email": "x@example.com", "name": "X", "password": PASSWORD}, expect=404)
    s, _ = Panel().call("POST", "/auth/login", {"email": EMAIL, "password": "wrong password!!"})
    if s != 401:
        fail(f"wrong password gave {s}")
    p.login()
    ok("AC-AUTH-03: setup once, second call 404, login works")

    _, proj = p.call("POST", "/projects", {"name": "Demo"}, expect=201)
    _, envs = p.call("GET", f"/projects/{proj['id']}/environments", expect=200)
    env = next(e for e in envs if e["kind"] == "production")
    _, svc = p.call("POST", f"/environments/{env['id']}/services",
                    {"name": "web", "type": "app", "source": {"image": "nginx:alpine"}, "config": {"image": "nginx:alpine", "port": 80}}, expect=201)
    _, dep = p.call("POST", f"/services/{svc['id']}/deploy", {}, expect=202)

    def live():
        _, d = p.call("GET", f"/deployments/{dep['id']}", expect=200)
        if d["state"] in ("failed", "cancelled"):
            fail(f"deployment {d['state']}: {d.get('failure_reason')} {d.get('failure_detail')}")
        return d["state"] == "live"
    wait("deployment live", live, timeout=240)
    ok("image service deployed (recreate), state live")
    _, before = p.call("GET", f"/services/{svc['id']}/deployments", expect=200)

    dns_add(APP)
    _, dom = p.call("POST", f"/services/{svc['id']}/domains", {"host": APP, "container_port": 80}, expect=201)
    # AC-DOM-03: no new deployment or release.
    _, after = p.call("GET", f"/services/{svc['id']}/deployments", expect=200)
    if len(after["items"]) != len(before["items"]):
        fail("a domain change created a deployment")
    ok("AC-DOM-03: adding a domain made no deployment")

    def issued():
        _, ds = p.call("GET", f"/services/{svc['id']}/domains", expect=200)
        d = next(d for d in ds if d["host"] == APP)
        if d["tls_status"] == "failed":
            fail(f"tls failed: {d.get('tls_error')}")
        return d["tls_status"] == "issued"
    wait("tls issued", issued, timeout=180)
    ctx = trust_context()
    status, body = https_get(APP, ctx)
    if status != 200 or "nginx" not in body.lower():
        fail(f"https app: {status}")
    code, loc, _ = http_get(APP)
    if code not in (301, 302, 307, 308) or not (loc or "").startswith("https://"):
        fail(f"http must redirect to https, got {code} {loc}")
    ok("AC-DOM-01: https://app.test serves the app with a valid certificate; http redirects")

    # AC-DOM-02: the same host on another service -> 409 domain_in_use.
    _, svc2 = p.call("POST", f"/environments/{env['id']}/services",
                     {"name": "other", "type": "app", "source": {"image": "nginx:alpine"}, "config": {"image": "nginx:alpine", "port": 80}}, expect=201)
    s, err = p.call("POST", f"/services/{svc2['id']}/domains", {"host": APP, "container_port": 80})
    if s != 409 or "domain_in_use" not in json.dumps(err):
        fail(f"AC-DOM-02: {s} {err}")
    ok("AC-DOM-02: 409 domain_in_use")

    # AC-APP-05 and AC-DOM-04: stop -> 503 Polipan page, release stays; start -> serves again.
    p.call("POST", f"/services/{svc['id']}/stop", {}, expect=202)
    def stopped():
        try:
            return https_get(APP, ctx)[0] == 503
        except OSError:
            return False
    wait("503 after stop", stopped, timeout=60)
    _, page = https_get(APP, ctx)
    if "caddy" in page.lower().split("\r\n\r\n", 1)[-1].lower():
        fail("the 503 page must be the Polipan page")
    _, sv = p.call("GET", f"/services/{svc['id']}", expect=200)
    if not sv.get("live_release_id"):
        fail("release must stay after stop")
    p.call("POST", f"/services/{svc['id']}/start", {}, expect=202)
    wait("200 after start", lambda: https_get(APP, ctx)[0] == 200, timeout=90)
    ok("AC-APP-05 and AC-DOM-04: stop gives 503, release stays, start serves again")

    # AC-DOM-05: DNS does not resolve -> tls_status failed with the error text in the API.
    p.call("POST", f"/services/{svc['id']}/domains", {"host": BAD, "container_port": 80}, expect=201)
    def failed():
        _, ds = p.call("GET", f"/services/{svc['id']}/domains", expect=200)
        d = next(d for d in ds if d["host"] == BAD)
        return d if d["tls_status"] == "failed" else None
    d = wait("tls failed for a bad domain", failed, timeout=180)
    if not d.get("tls_error"):
        fail("AC-DOM-05: error text is empty")
    ok("AC-DOM-05: tls_status failed with error text: " + d["tls_error"][:80])

    json.dump({"service_id": svc["id"]}, open(STATE, "w"))
    print("FLOW PASSED")


def serving():
    ctx = trust_context()
    # A killed app container is restarted by the agent on its own, so allow a short wait.
    def up():
        try:
            return https_get(APP, ctx)[0] == 200
        except OSError:
            return False
    wait("app serves while the server is down", up, timeout=90, every=3)
    ok("app still serves over https")


def after():
    p = Panel()
    p.login()
    st = json.load(open(STATE))
    _, sv = p.call("GET", f"/services/{st['service_id']}", expect=200)
    if sv.get("desired_state") not in (None, "running"):
        fail(f"service state {sv.get('desired_state')}")
    _, nodes = p.call("GET", "/nodes", expect=200)
    if not nodes or nodes[0]["state"] != "ready":
        fail(f"node is not ready: {nodes}")
    # The heartbeat must be newer than the server restart: the agent really reconnected.
    since = int(sys.argv[2]) if len(sys.argv) > 2 else 0
    beat = time.mktime(time.strptime(nodes[0]["last_heartbeat_at"][:19], "%Y-%m-%dT%H:%M:%S")) - time.timezone
    if beat < since:
        fail(f"no heartbeat since the restart: {nodes[0]['last_heartbeat_at']}")
    ok("server is back: login works, node is ready")


GIT_SERVER = "http://192.168.77.10:8000"
GIT_APP = "gitapp.test"


def push_hook(url, secret, sha, delivery):
    """Sends a GitHub style push webhook (X-Hub-Signature-256) to the panel through Caddy."""
    import hashlib
    import hmac
    body = json.dumps({"ref": "refs/heads/main", "after": sha, "repository": {"full_name": "app"},
                       "pusher": {"name": "ann"}}).encode()
    sig = "sha256=" + hmac.new(secret.encode(), body, hashlib.sha256).hexdigest()
    path = url.split(PANEL, 1)[1]
    conn = http.client.HTTPConnection("127.0.0.1", 80, timeout=30)
    conn.request("POST", path, body, {"Host": PANEL, "Content-Type": "application/json", "X-GitHub-Event": "push",
                                      "X-GitHub-Delivery": delivery, "X-Hub-Signature-256": sig})
    r = conn.getresponse()
    return r.status, json.loads(r.read() or b"{}")


def gitflow():
    """T-E2E-02 (without the rollback part, M3): a Git app deploys on every push from a fake Git
    server; 3 pushes give 3 releases with different digests; a bad signature is refused (AC-APP-03)."""
    p = Panel()
    p.login()
    _, projects = p.call("GET", "/projects", expect=200)
    proj = projects["items"][0]
    _, envs = p.call("GET", f"/projects/{proj['id']}/environments", expect=200)
    env = next(e for e in envs if e["kind"] == "staging")
    _, svc = p.call("POST", f"/environments/{env['id']}/services", {
        "name": "gitapp", "type": "app",
        "source": {"type": "git", "repo": GIT_SERVER + "/app.git", "branch": "main", "builder": "dockerfile", "provider": "github"},
        "config": {"port": 80}}, expect=201)
    _, key = p.call("POST", f"/services/{svc['id']}/deploy-key", expect=201)
    p.call("POST", f"/services/{svc['id']}/domains", {"host": GIT_APP, "container_port": 80, "https": False}, expect=201)
    ok("git service with webhook " + key["webhook_url"])

    status, _ = push_hook(key["webhook_url"], "wrong-secret", "0" * 39 + "1", "bad-1")
    if status != 401:
        fail(f"bad signature gave {status}")
    _, deps = p.call("GET", f"/services/{svc['id']}/deployments", expect=200)
    if deps["items"]:
        fail("a bad signature queued a deployment")
    ok("AC-APP-03: bad signature 401, nothing queued")

    digests = []
    for version in (1, 2, 3):
        sha = urllib.request.urlopen(urllib.request.Request(f"{GIT_SERVER}/commit?version={version}", b"", method="POST"), timeout=30).read().decode()
        status, out = push_hook(key["webhook_url"], key["webhook_secret"], sha, f"push-{version}")
        if status != 202 or len(out.get("deployments", [])) != 1:
            fail(f"push {version}: {status} {out}")
        dep_id = out["deployments"][0]

        def live():
            _, d = p.call("GET", f"/deployments/{dep_id}", expect=200)
            if d["state"] in ("failed", "cancelled"):
                _, logs = p.call("GET", f"/deployments/{dep_id}/logs", expect=200)
                print("\n".join(l["line"] for l in logs[-30:]))
                fail(f"deployment {d['state']}: {d.get('failure_reason')} {d.get('failure_detail')}")
            return d if d["state"] == "live" else None
        d = wait(f"git deployment {version} live", live, timeout=600, every=3)
        if d["trigger"] != "git_push":
            fail(f"trigger {d['trigger']}")
        wait(f"app serves version {version}", lambda: f"version {version}" in http_get(GIT_APP)[2], timeout=90)
        _, svc_now = p.call("GET", f"/services/{svc['id']}", expect=200)
        digests.append(svc_now["live_release_id"])
        ok(f"push {version}: built, released and serving (commit {sha[:8]})")
        if version == 1:
            status, _ = push_hook(key["webhook_url"], key["webhook_secret"], sha, "push-1")
            if status != 409:
                fail(f"replayed delivery gave {status}")
            ok("replayed delivery refused")
    if len(set(digests)) != 3:
        fail(f"want 3 releases, got {digests}")
    _, deps = p.call("GET", f"/services/{svc['id']}/deployments", expect=200)
    if len([d for d in deps["items"] if d["state"] == "live"]) != 3:
        fail("want 3 live deployments")
    ok("T-E2E-02: 3 pushes gave 3 releases (rollback part is M3)")
    print("GITFLOW PASSED")


def rollback():
    """T-E2E-02, rollback part: after 3 pushes, one click goes back to v2. The kept stopped containers
    of v2 are started, so no image is pulled and no build runs, and traffic switches in under 10 s (AC-REL-01, 02, 04, 07)."""
    p = Panel()
    p.login()
    _, projects = p.call("GET", "/projects", expect=200)
    _, envs = p.call("GET", f"/projects/{projects['items'][0]['id']}/environments", expect=200)
    env = next(e for e in envs if e["kind"] == "staging")
    _, svcs = p.call("GET", f"/environments/{env['id']}/services", expect=200)
    svc = next(s for s in svcs if s["name"] == "gitapp")
    _, rels = p.call("GET", f"/services/{svc['id']}/releases", expect=200)
    items = rels["items"]  # newest first
    if len(items) != 3 or items[0]["status"] != "live":
        fail(f"releases: {[(r['number'], r['status']) for r in items]}")
    v3, v2, v1 = items
    if v2["status"] != "previous" or not v2["fast_rollback_possible"]:
        fail(f"v2 must have kept containers: {v2['status']} {v2['fast_rollback_possible']}")
    if v1["fast_rollback_possible"]:
        fail("v1 must not have kept containers (retain_stopped is 1)")
    s, err = p.call("POST", f"/services/{svc['id']}/rollback", {"release_id": v3["id"]})
    if s != 409 or err["code"] != "already_live":
        fail(f"AC-REL-07: {s} {err}")
    ok("AC-REL-07: rollback to the live release gives 409 already_live")
    t0 = time.time()
    _, dep = p.call("POST", f"/services/{svc['id']}/rollback", {"release_id": v2["id"]}, expect=202)
    wait("traffic on v2", lambda: "version 2" in http_get(GIT_APP)[2], timeout=60, every=0.2)
    took = time.time() - t0
    if took >= 10:
        fail(f"fast rollback took {took:.1f} s")
    ok(f"AC-REL-02: fast rollback switched traffic to v2 in {took:.1f} s")
    d = wait("rollback live", lambda: (lambda x: x if x["state"] == "live" else None)(p.call("GET", f"/deployments/{dep['id']}", expect=200)[1]), timeout=120)
    if d["trigger"] != "rollback" or d["release_id"] != v2["id"]:
        fail(f"rollback deployment: {d}")
    _, logs = p.call("GET", f"/deployments/{dep['id']}/logs", expect=200)
    if any(l["stream"] == "build" for l in logs):
        fail("AC-REL-04: a build ran in the rollback")
    steps = [x["message"] for x in d["steps"]]
    if any("pull" in m.lower() for m in steps):
        fail(f"a pull ran in the fast rollback: {steps}")
    _, sv = p.call("GET", f"/services/{svc['id']}", expect=200)
    if sv["live_release_id"] != v2["id"]:
        fail("live release is not v2")
    ok("AC-REL-01, AC-REL-04: rollback deployment is live on v2 with no build")
    print("ROLLBACK PASSED")


ZD = "zd.test"
ZD_SCRIPT = r"""
import os, time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
V = os.environ.get("VERSION", "0")
START = time.time()
DELAY = float(os.environ.get("READY_AFTER", "0"))
FAIL = os.environ.get("FAIL") == "1"
broken = [False]
class H(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    def log_message(self, *a): pass
    def reply(self, code, body):
        b = body.encode()
        self.send_response(code); self.send_header("Content-Length", str(len(b))); self.end_headers(); self.wfile.write(b)
    def do_GET(self):
        p = self.path.split("?")[0]
        if p == "/ready":
            if broken[0]: self.reply(500, "broken")
            elif FAIL or time.time() - START < DELAY: self.reply(503, "not ready yet")
            else: self.reply(200, "ready")
        elif p == "/_break":
            broken[0] = True; self.reply(200, "broken")
        elif p == "/ws":
            self.send_response(101); self.send_header("Upgrade", "websocket"); self.send_header("Connection", "Upgrade"); self.end_headers()
            try:
                while True:
                    self.wfile.write(("v" + V + "\n").encode()); self.wfile.flush(); time.sleep(0.2)
            except Exception:
                pass
            self.close_connection = True
        else:
            self.reply(200, "version " + V)
ThreadingHTTPServer(("0.0.0.0", 8000), H).serve_forever()
"""


class Load:
    """Sends `rate` requests per second to the app through Caddy. Every request has its own connection."""

    def __init__(self, host, rate=20):
        import threading
        self.host, self.rate, self.log, self.lock = host, rate, [], threading.Lock()
        self.stop_flag = threading.Event()
        self.thread = threading.Thread(target=self.run, daemon=True)

    def one(self):
        t0 = time.time()
        try:
            conn = http.client.HTTPConnection("127.0.0.1", 80, timeout=10)
            conn.request("GET", "/", headers={"Host": self.host, "Connection": "close"})
            r = conn.getresponse()
            rec = (t0, r.status, r.read().decode("latin1"))
            conn.close()
        except Exception as e:  # a failed request is a result, not a crash
            rec = (t0, 0, repr(e))
        with self.lock:
            self.log.append(rec)

    def run(self):
        import threading
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
        time.sleep(1.5)  # let the last requests end
        return list(self.log)

    def first_seen(self, version):
        with self.lock:
            for t, status, body in self.log:
                if status == 200 and body == f"version {version}":
                    return t
        return None


def check_load(log, what):
    bad = [r for r in log if r[1] != 200]
    if bad:
        fail(f"{what}: {len(bad)} of {len(log)} requests failed, first: {bad[0]}")
    ok(f"{what}: {len(log)} requests, none failed")


def check_cutover(log, old, new):
    """The answer changes from old to new at one moment: after the first request that got `new`, no later request gets `old`."""
    firsts = [t for t, s, b in log if b == f"version {new}"]
    if not firsts:
        fail(f"version {new} never answered")
    first = min(firsts)
    late = [r for r in log if r[2] == f"version {old}" and r[0] > first + 0.3]
    if late:
        fail(f"version {old} answered after {new} started: {late[0]}")


def deploy_wait(p, svc_id, state="live", timeout=300, body=None):
    _, dep = p.call("POST", f"/services/{svc_id}/deploy", body or {}, expect=202)

    def done():
        _, d = p.call("GET", f"/deployments/{dep['id']}", expect=200)
        return d if d["state"] in ("live", "failed", "rolled_back", "cancelled") else None
    d = wait(f"deployment {dep['id']} ends", done, timeout=timeout, every=1)
    if d["state"] != state:
        fail(f"deployment {d['state']} ({d.get('failure_reason')}: {d.get('failure_detail')}), want {state}")
    return d


def step_time(d, state):
    for st in d["steps"]:
        if st["state"] == state:
            return time.mktime(time.strptime(st["time"][:19], "%Y-%m-%dT%H:%M:%S"))
    return None


def zd_service(p):
    _, projects = p.call("GET", "/projects", expect=200)
    proj = projects["items"][0]
    _, envs = p.call("GET", f"/projects/{proj['id']}/environments", expect=200)
    env = next(e for e in envs if e["kind"] == "production")
    _, svcs = p.call("GET", f"/environments/{env['id']}/services", expect=200)
    return proj, next(s for s in svcs if s["name"] == "zd")


def set_vars(p, proj, svc_id, values):
    _, existing = p.call("GET", f"/projects/{proj['id']}/variables?service_id={svc_id}", expect=200)
    by_name = {v["name"]: v for v in existing}
    for name, value in values.items():
        if name in by_name:
            p.call("PATCH", f"/variables/{by_name[name]['id']}", {"name": name, "value": value, "service_id": svc_id}, expect=200)
        else:
            p.call("POST", f"/projects/{proj['id']}/variables", {"name": name, "value": value, "service_id": svc_id}, expect=201)


def events_reader(p, topics):
    """Reads the SSE stream of platform events in a thread. Returns (list of events, stop function)."""
    import threading
    out, stop = [], threading.Event()

    def run():
        try:
            conn = http.client.HTTPConnection("127.0.0.1", 80, timeout=None)  # a read timeout would break the reader for good
            conn.request("GET", "/api/v1/events?topics=" + topics, headers={"Host": PANEL, "Cookie": p.cookie})
            r = conn.getresponse()
            while not stop.is_set():
                line = r.fp.readline()
                if not line:
                    break
                if line.startswith(b"data: "):
                    out.append(json.loads(line[6:]))
        except OSError:
            pass
    t = threading.Thread(target=run, daemon=True)
    t.start()
    return out, stop.set


def zerodowntime():
    """T-E2E-03: 20 requests per second while blue-green deploys 5 times. No failed request. A failing
    health check keeps blue serving. Also AC-HC-01, AC-HC-05, AC-DEPLOY-09, AC-HC-03, AC-HC-04."""
    p = Panel()
    p.login()
    _, projects = p.call("GET", "/projects", expect=200)
    proj = projects["items"][0]
    _, envs = p.call("GET", f"/projects/{proj['id']}/environments", expect=200)
    env = next(e for e in envs if e["kind"] == "production")
    config = {"image": "python:3.12-slim", "port": 8000, "command": ["python", "-u", "-c", ZD_SCRIPT],
              "health": {"type": "http", "path": "/ready", "interval": 1, "timeout": 2, "healthy_threshold": 3, "unhealthy_threshold": 3,
                         "start_period": 5, "deploy_timeout": 25},
              "strategy_options": {"drain_seconds": 10}}
    _, svc = p.call("POST", f"/environments/{env['id']}/services", {"name": "zd", "type": "app", "source": {"image": config["image"]}, "config": config}, expect=201)
    p.call("POST", f"/services/{svc['id']}/domains", {"host": ZD, "container_port": 8000, "https": False}, expect=201)
    set_vars(p, proj, svc["id"], {"VERSION": "1", "READY_AFTER": "0", "FAIL": "0"})
    d = deploy_wait(p, svc["id"])
    if d["strategy"] != "blue_green":
        fail(f"strategy {d['strategy']}")
    wait("v1 serves", lambda: http_get(ZD)[2] == "version 1", timeout=30)
    ok("zd service: first deploy live on version 1 (blue_green)")

    load = Load(ZD).start()
    for v in range(2, 7):
        set_vars(p, proj, svc["id"], {"VERSION": str(v)})
        d = deploy_wait(p, svc["id"])
        if d["strategy"] != "blue_green":
            fail(f"strategy {d['strategy']}")
        wait(f"version {v}", lambda: load.first_seen(v), timeout=30, every=0.2)
    log = load.stop()
    check_load(log, "AC-DEPLOY-01: 5 blue-green deploys under 20 requests/s")
    for v in range(2, 7):
        check_cutover(log, v - 1, v)
    ok("AC-DEPLOY-01: the answer changed from old to new at one moment, 5 times")

    # AC-HC-01: /ready is 503 for 8 s after the start. The switch comes only after three 200s.
    set_vars(p, proj, svc["id"], {"VERSION": "7", "READY_AFTER": "8"})
    d = deploy_wait(p, svc["id"])
    gap = step_time(d, "switching") - step_time(d, "starting")
    if gap < 9:
        fail(f"AC-HC-01: switched {gap} s after the start of green, /ready was 503 for 8 s")
    ok(f"AC-HC-01: switch {gap:.0f} s after green started (503 for 8 s, then three 200s)")
    set_vars(p, proj, svc["id"], {"READY_AFTER": "0"})

    # AC-DEPLOY-02 and AC-HC-05: green never gets healthy. Blue keeps serving. The error shows status and body.
    load = Load(ZD).start()
    set_vars(p, proj, svc["id"], {"VERSION": "8", "FAIL": "1"})
    d = deploy_wait(p, svc["id"], state="failed")
    if d["failure_reason"] != "health_timeout" or "503" not in d["failure_detail"] or "not ready yet" not in d["failure_detail"]:
        fail(f"AC-HC-05: {d['failure_reason']}: {d['failure_detail']}")
    log = load.stop()
    check_load(log, "AC-DEPLOY-02: failed deploy, blue kept serving")
    if any(b == "version 8" for _, _, b in log):
        fail("the failed version answered")
    ok("AC-DEPLOY-02, AC-HC-05: health_timeout with the last status and body: " + d["failure_detail"][:90])
    set_vars(p, proj, svc["id"], {"FAIL": "0"})

    # AC-DEPLOY-09: a stream stays open for drain_seconds after the switch.
    import socket
    import threading
    closed = []
    opened = time.time()
    sock = socket.create_connection(("127.0.0.1", 80), timeout=60)
    sock.sendall(f"GET /ws HTTP/1.1\r\nHost: {ZD}\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n".encode())

    def read_ws():
        try:
            while sock.recv(4096):
                pass
        except OSError:
            pass
        closed.append(time.time())
    threading.Thread(target=read_ws, daemon=True).start()
    time.sleep(1)
    load = Load(ZD).start()
    set_vars(p, proj, svc["id"], {"VERSION": "9"})
    deploy_wait(p, svc["id"])
    log = load.stop()
    switched = load.first_seen(9)
    if not closed:
        wait("stream closes", lambda: bool(closed), timeout=60)
    if closed[0] - switched < 9:
        fail(f"AC-DEPLOY-09: the stream closed {closed[0] - switched:.1f} s after the switch (drain_seconds is 10)")
    ok(f"AC-DEPLOY-09: the open stream closed {closed[0] - switched:.1f} s after the switch (drain_seconds 10)")
    check_load(log, "deploy with an open stream")

    # AC-HC-03 and AC-HC-04: the live container turns unhealthy. With one replica all are unhealthy:
    # Caddy keeps routing, the alert is critical, the agent restarts the container and it rejoins.
    events, stop_events = events_reader(p, "alert")
    time.sleep(1)
    http_get(ZD, "/_break")
    def unhealthy():
        _, sv = p.call("GET", f"/services/{svc['id']}", expect=200)
        return any(pl["role"] == "live" and pl["health"] == "unhealthy" for pl in sv["placements"])
    wait("live placement unhealthy", unhealthy, timeout=40, every=1)
    code, _, body = http_get(ZD)
    if code != 200:
        fail(f"AC-HC-04: routing stopped: {code}")
    wait("critical alert", lambda: any(e["payload"].get("severity") == "critical" and e["payload"].get("state") == "firing" for e in events), timeout=20, every=1)
    ok("AC-HC-03, AC-HC-04: unhealthy live container: still routed (200), critical alert fired")
    def healthy_again():
        _, sv = p.call("GET", f"/services/{svc['id']}", expect=200)
        return any(pl["role"] == "live" and pl["health"] == "healthy" for pl in sv["placements"])
    wait("restarted and healthy again", healthy_again, timeout=90, every=2)
    wait("alert resolved", lambda: any(e["payload"].get("state") == "resolved" for e in events), timeout=20, every=1)
    stop_events()
    ok("REQ-HC-007: the agent restarted the container, it is healthy again, the alert resolved")
    json.dump({"service_id": svc["id"], "project_id": proj["id"]}, open("/e2e/zd.json", "w"))
    print("ZERODOWNTIME PASSED")


def chaos_start():
    """Starts a deployment of zd and waits for health_checking (the shell pauses Caddy then, so the switch hangs)."""
    mode = sys.argv[2]
    p = Panel()
    p.login()
    proj, svc = zd_service(p)
    set_vars(p, proj, svc["id"], {"VERSION": "10" if mode == "switching" else "11", "READY_AFTER": "0" if mode == "switching" else "300"})
    _, dep = p.call("POST", f"/services/{svc['id']}/deploy", {}, expect=202)
    json.dump({"deployment_id": dep["id"], "service_id": svc["id"]}, open("/e2e/chaos.json", "w"))
    if mode == "switching":
        def hc():
            _, d = p.call("GET", f"/deployments/{dep['id']}", expect=200)
            return d["state"] == "health_checking"
        wait("health_checking", hc, timeout=120, every=0.3)
    else:
        def hc():
            _, d = p.call("GET", f"/deployments/{dep['id']}", expect=200)
            return d["state"] == "health_checking"
        wait("health_checking", hc, timeout=120, every=0.5)
    ok(f"chaos {mode}: deployment {dep['id']} is in health_checking")


def chaos_end():
    """After the server restart: the deployment is in a terminal state (REQ-DEPLOY-107) and the service answers."""
    mode, want = sys.argv[2], sys.argv[3]
    st = json.load(open("/e2e/chaos.json"))
    p = Panel()
    wait("login after restart", lambda: (p.login() or True), timeout=120, every=3)

    def done():
        _, d = p.call("GET", f"/deployments/{st['deployment_id']}", expect=200)
        return d if d["state"] in ("live", "failed", "rolled_back", "cancelled") else None
    d = wait("deployment ends after the restart", done, timeout=180, every=2)
    if d["state"] != want:
        fail(f"chaos {mode}: deployment is {d['state']} ({d.get('failure_reason')}), want {want}")
    version = "10"  # the killed deploy of the health round never became live
    wait(f"app serves version {version}", lambda: http_get(ZD)[2] == f"version {version}", timeout=60)
    _, sv = p.call("GET", f"/services/{st['service_id']}", expect=200)
    live = [pl for pl in sv["placements"] if pl["role"] == "live"]
    if len(live) != 1 or any(pl["role"] in ("candidate", "draining") for pl in sv["placements"]) and mode != "switching":
        fail(f"placements after the restart: {[(pl['role'], pl['color']) for pl in sv['placements']]}")
    ok(f"REQ-DEPLOY-107: after the server kill in {mode} the deployment ended {d['state']} and version {version} serves")
    print("CHAOS PASSED")



# ---- M4: databases, templates, backups (run-m4.sh) ----

M4 = "/e2e/m4.json"
MINIO = {"endpoint": "http://127.0.0.1:9000", "bucket": "backups", "path": "pp"}
MINIO_CREDS = {"access_key_id": "minio", "secret_access_key": "minio-secret-123"}


def m4_state():
    return json.load(open(M4))


def m4_save(**kv):
    st = m4_state() if __import__("os").path.exists(M4) else {}
    st.update(kv)
    json.dump(st, open(M4, "w"))


def m4_panel():
    p = Panel()
    p.login()
    return p


def wait_run(p, policy_id, want="succeeded", timeout=240, newer_than=None):
    def done():
        _, runs = p.call("GET", f"/backup-policies/{policy_id}/runs", expect=200)
        for r in runs["items"]:
            if newer_than and r["id"] in newer_than:
                continue
            if r["state"] in ("succeeded", "failed"):
                return r
        return None
    r = wait(f"backup run of {policy_id} ends", done, timeout=timeout, every=2)
    if r["state"] != want:
        fail(f"run {r['id']} is {r['state']}: {r.get('error')}")
    return r


def wait_check(p, policy_id, want, known=(), timeout=300):
    def done():
        _, cs = p.call("GET", f"/restore-checks?policy_id={policy_id}", expect=200)
        for c in cs["items"]:
            if c["id"] not in known and c["state"] in ("passed", "failed"):
                return c
        return None
    c = wait(f"restore check of {policy_id} ends", done, timeout=timeout, every=3)
    if c["state"] != want:
        fail(f"restore check {c['id']} is {c['state']}: {json.dumps(c.get('checks'))}")
    return c


def m4_setup():
    """AC-DB-01, AC-DB-02, AC-DB-03, AC-DB-04 in a real stack, and the two backup targets (MinIO and SFTP)."""
    p = Panel()
    wait("panel behind caddy", lambda: http_get(PANEL, "/healthz")[0] == 200, timeout=180)
    token = re.search(r"token=([0-9a-f]+)", open("/e2e/setup-url").read()).group(1)
    p.call("POST", "/setup/first-user", {"setup_token": token, "email": EMAIL, "name": "Owner", "password": PASSWORD}, expect=201)
    p.login()
    _, proj = p.call("POST", "/projects", {"name": "Shop"}, expect=201)
    _, envs = p.call("GET", f"/projects/{proj['id']}/environments", expect=200)
    env = next(e for e in envs if e["kind"] == "production")
    _, db = p.call("POST", f"/environments/{env['id']}/services", {"name": "main", "type": "database", "db_engine": "postgres"}, expect=201)
    if db["backup"]["status"] != "none" or "host_port" in db["config"]:
        fail(f"AC-DB-03: {db['backup']} {db['config']}")
    ok("AC-DB-03: a new database has no host port and the UI gets a backup suggestion")
    _, plan = p.call("POST", f"/services/{db['id']}/deploy", {"dry_run": True, "strategy": "blue_green"}, expect=200)
    if plan["strategy"] != "recreate" or "zero_downtime_not_available" not in json.dumps(plan):
        fail(f"AC-DB-02: {plan}")
    ok("AC-DB-02: blue_green on a database is the forced recreate with the warning")
    deploy_wait(p, db["id"])
    _, secrets = p.call("GET", f"/projects/{proj['id']}/secrets?service_id={db['id']}", expect=200)
    if not any(x["name"] == "DB_PASSWORD" for x in secrets) or "value" in json.dumps(secrets):
        fail(f"AC-DB-01: secrets {secrets}")
    # AC-DB-04: a service that points to a database that does not exist fails and nothing deploys.
    _, bad = p.call("POST", f"/environments/{env['id']}/services",
                    {"name": "bad", "type": "app", "source": {"image": "nginx:alpine"}, "config": {"image": "nginx:alpine", "port": 80}}, expect=201)
    p.call("POST", f"/projects/{proj['id']}/variables", {"name": "DATABASE_URL", "value": "${{db.missing.URL}}", "service_id": bad["id"]}, expect=201)
    d = deploy_wait(p, bad["id"], state="failed")
    if d["failure_reason"] != "unresolved_reference":
        fail(f"AC-DB-04: {d}")
    _, sv = p.call("GET", f"/services/{bad['id']}", expect=200)
    if sv.get("live_release_id"):
        fail("AC-DB-04: a release was made")
    ok("AC-DB-04: unresolved_reference, no release, nothing deployed")
    # AC-DB-01: a second service resolves ${{db.main.URL}} after a new release.
    _, api = p.call("POST", f"/environments/{env['id']}/services",
                    {"name": "api", "type": "app", "source": {"image": "nginx:alpine"}, "config": {"image": "nginx:alpine", "port": 80}}, expect=201)
    p.call("POST", f"/projects/{proj['id']}/variables", {"name": "DATABASE_URL", "value": "${{db.main.URL}}", "service_id": api["id"]}, expect=201)
    deploy_wait(p, api["id"])
    ok("AC-DB-01: DB_PASSWORD is a secret without a value in the API, and ${{db.main.URL}} resolved after a new release")

    # The targets: MinIO (S3) and SFTP, both real servers in the test Docker host.
    _, minio = p.call("POST", "/backup-targets", {"name": "minio", "kind": "s3", "config": MINIO, "credentials": MINIO_CREDS}, expect=201)
    key = open("/e2e/sftp/id_ed25519").read()
    _, sftp = p.call("POST", "/backup-targets", {"name": "sftp", "kind": "sftp",
                     "config": {"host": "127.0.0.1", "port": 2222, "user": "bk", "path": "/srv/restic"}, "credentials": {"private_key": key}}, expect=201)
    for t in (minio, sftp):
        _, res = p.call("POST", f"/backup-targets/{t['id']}/test", expect=200)
        if not res["ok"]:
            fail(f"target {t['name']} test: {res}")
    ok("both targets (MinIO and SFTP) pass the test action")
    m4_save(project=proj["id"], env=env["id"], db=db["id"], api=api["id"], minio=minio["id"], sftp=sftp["id"])
    print("M4 SETUP PASSED")


def m4_backup():
    """AC-BKP-01 (two targets, one snapshot on each), AC-BKP-03 (manual run), AC-BKP-07 (the check restores in a temporary container)."""
    p = m4_panel()
    st = m4_state()
    _, pol = p.call("POST", f"/services/{st['db']}/backup-policies",
                    {"schedule": "0 2 * * *", "target_ids": [st["minio"], st["sftp"]], "retention": {"keep_daily": 7}}, expect=201)
    _, run = p.call("POST", f"/backup-policies/{pol['id']}/run", expect=202)
    r = wait_run(p, pol["id"])
    snaps = {s["target_id"]: s for s in r["snapshots"]}
    if set(snaps) != {st["minio"], st["sftp"]} or not all(s["ok"] and s["snapshot_id"] for s in snaps.values()):
        fail(f"AC-BKP-01: {r['snapshots']}")
    if r["trigger"] != "manual" or not r["manifest"].get("tables"):
        fail(f"run: {r}")
    ok(f"AC-BKP-01 and AC-BKP-03: manual run, one snapshot on each target, manifest {r['manifest']}")
    c = wait_check(p, pol["id"], "passed")
    names = [x["name"] for x in c["checks"]]
    if c["trigger"] != "first_backup" or "table_count" not in names or "row_estimate" not in names or c["target_id"] != st["minio"]:
        fail(f"AC-BKP-07: {c}")
    ok(f"AC-BKP-07: the restore check ran by itself after the first backup and passed: {names}")
    m4_save(policy=pol["id"], run=r["id"], check1=c["id"])
    _, got = p.call("GET", f"/backup-policies/{pol['id']}", expect=200)
    if got["health"] != "verified":
        fail(f"health: {got['health']}")
    print("M4 BACKUP PASSED")


def m4_restore():
    """AC-BKP-04 (restore into a new service), the restore in place with a safety backup."""
    p = m4_panel()
    st = m4_state()
    run_id = st["run"]
    _, out = p.call("POST", f"/backup-runs/{run_id}/restore", {"target_id": st["minio"], "mode": "new_service", "new_service_name": "main-copy"}, expect=202)

    def done():
        _, j = p.call("GET", f"/backup-restores/{out['job_id']}", expect=200)
        return j if j["state"] in ("succeeded", "failed") else None
    j = wait("restore into a new service", done, timeout=420, every=3)
    if j["state"] != "succeeded":
        fail(f"AC-BKP-04: {j}")
    m4_save(copy=out["service_id"])
    ok("AC-BKP-04: the snapshot was restored into the new service main-copy")
    print("M4 RESTORE PASSED")


def m4_restore_inplace():
    p = m4_panel()
    st = m4_state()
    _, runs = p.call("GET", f"/backup-policies/{st['policy']}/runs", expect=200)
    _, out = p.call("POST", f"/backup-runs/{st['run']}/restore", {"target_id": st["sftp"], "mode": "in_place", "confirm": "main"}, expect=202)

    def done():
        _, j = p.call("GET", f"/backup-restores/{out['job_id']}", expect=200)
        return j if j["state"] in ("succeeded", "failed") else None
    j = wait("restore in place", done, timeout=300, every=3)
    if j["state"] != "succeeded":
        fail(f"restore in place: {j}")
    _, runs = p.call("GET", f"/backup-policies/{st['policy']}/runs", expect=200)
    if not any(r["trigger"] == "safety" and r["state"] == "succeeded" for r in runs["items"]):
        fail("no safety backup before the restore")
    ok("restore in place from the SFTP target worked, after a safety backup")
    print("M4 INPLACE PASSED")


def m4_check_corrupt():
    """AC-BKP-08: the snapshot on the SFTP target was corrupted: the check fails and a critical alert is raised."""
    p = m4_panel()
    st = m4_state()
    _, cs = p.call("GET", f"/restore-checks?policy_id={st['policy']}", expect=200)
    known = [c["id"] for c in cs["items"]]
    _, chk = p.call("POST", f"/backup-policies/{st['policy']}/restore-checks", expect=202)
    c = wait_check(p, st["policy"], "failed", known=known)
    if c["target_id"] != st["sftp"]:
        fail(f"the check should rotate to the SFTP target: {c['target_id']}")
    ok(f"AC-BKP-08: the check of the corrupted snapshot failed: {json.dumps(c['checks'])[:200]}")
    m4_save(failed_check=c["id"])
    print("M4 CORRUPT PASSED")


def m4_volume():
    """AC-BKP-09: a volume policy: the restore check restores to a temporary folder and compares file count and size."""
    p = m4_panel()
    st = m4_state()
    _, files = p.call("POST", f"/environments/{st['env']}/services",
                      {"name": "files", "type": "app", "source": {"image": "nginx:alpine"}, "config": {"image": "nginx:alpine", "port": 80}}, expect=201)
    _, vol = p.call("POST", f"/services/{files['id']}/volumes", {"name": "data", "mount_path": "/data"}, expect=201)
    deploy_wait(p, files["id"])
    m4_save(files=files["id"], volume=vol["id"])
    print("M4 VOLUME STARTED")


def m4_volume_backup():
    p = m4_panel()
    st = m4_state()
    _, pol = p.call("POST", f"/services/{st['files']}/backup-policies",
                    {"volume_id": st["volume"], "target_ids": [st["minio"]], "retention": {"keep_daily": 7}}, expect=201)
    p.call("POST", f"/backup-policies/{pol['id']}/run", expect=202)
    r = wait_run(p, pol["id"])
    if r["manifest"].get("files") != 200:
        fail(f"manifest: {r['manifest']}")
    c = wait_check(p, pol["id"], "passed")
    names = {x["name"]: x for x in c["checks"]}
    if "file_count" not in names or "total_size" not in names or not names["file_count"]["ok"]:
        fail(f"AC-BKP-09: {c['checks']}")
    ok(f"AC-BKP-09: volume check passed: {names['file_count']['detail']}; {names['total_size']['detail']}")
    print("M4 VOLUME PASSED")


def m4_controlplane():
    """AC-BKP-06: the control-plane database is backed up daily by the built-in policy."""
    p = m4_panel()
    _, pols = p.call("GET", "/backup-policies", expect=200)
    cp = next((x for x in pols if x.get("control_plane")), None)
    if not cp or cp["schedule"] != "0 3 * * *" or not cp["local_only"]:
        fail(f"AC-BKP-06: {pols}")
    p.call("POST", f"/backup-policies/{cp['id']}/run", expect=202)
    r = wait_run(p, cp["id"])
    ok(f"AC-BKP-06: the control-plane policy exists (daily, local target with a warning) and its run succeeded: {r['snapshots']}")
    c = wait_check(p, cp["id"], "passed", timeout=300)
    ok(f"the restore check of the control-plane dump passed: {[x['name'] for x in c['checks']]}")
    m4_save(cp=cp["id"])
    print("M4 CONTROLPLANE PASSED")


def m4_template():
    """AC-TPL-01 in a real stack: WordPress and MySQL, the password is a generated secret, both go live."""
    p = m4_panel()
    st = m4_state()
    _, svcs = p.call("POST", "/templates/wordpress/instantiate", {"environment_id": st["env"], "inputs": {"domain": "blog.test"}}, expect=201)
    by = {s["name"]: s for s in svcs}
    if set(by) != {"wordpress", "wordpress-db"}:
        fail(f"services: {list(by)}")
    deploy_wait(p, by["wordpress-db"]["id"], timeout=420)
    deploy_wait(p, by["wordpress"]["id"], timeout=420)
    _, secrets = p.call("GET", f"/projects/{st['project']}/secrets?service_id={by['wordpress-db']['id']}", expect=200)
    if not any(x["name"] == "DB_PASSWORD" for x in secrets) or "value" in json.dumps(secrets):
        fail(f"secrets: {secrets}")
    ok("AC-TPL-01: WordPress and its MySQL are live, the database password is a generated secret")
    m4_save(wordpress=by["wordpress"]["id"], wordpress_db=by["wordpress-db"]["id"])
    print("M4 TEMPLATE PASSED")


if __name__ == "__main__":
    {"flow": flow, "serving": serving, "after": after, "gitflow": gitflow, "rollback": rollback, "zerodowntime": zerodowntime,
     "chaos-start": chaos_start, "chaos-end": chaos_end, "m4-setup": m4_setup, "m4-backup": m4_backup, "m4-restore": m4_restore,
     "m4-restore-inplace": m4_restore_inplace, "m4-check-corrupt": m4_check_corrupt, "m4-volume": m4_volume,
     "m4-volume-backup": m4_volume_backup, "m4-controlplane": m4_controlplane, "m4-template": m4_template}[sys.argv[1]]()
