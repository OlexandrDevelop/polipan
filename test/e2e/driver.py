# SPDX-License-Identifier: Apache-2.0
"""End-to-end driver (T-E2E-01, T-E2E-02 without rollback, T-E2E-13, AC-AUTH-03, AC-DOM-01..05, AC-APP-03, AC-APP-05).

It runs inside the test Docker host with host networking. The panel is reached through the
Caddy of the node (Host: panel.test), so the panel route is tested too.
Usage: driver.py flow | gitflow | serving | after
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


if __name__ == "__main__":
    {"flow": flow, "serving": serving, "after": after, "gitflow": gitflow}[sys.argv[1]]()
