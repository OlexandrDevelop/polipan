# SPDX-License-Identifier: Apache-2.0
"""End-to-end driver of milestone M9 (run-m9.sh): T-E2E-05 (promotion) and T-E2E-06 (preview environments),
AC-PREV-01 to 04, AC-PROMO-01, 02, 04, AC-VOL-04.

It runs inside the test Docker host (docker:dind) with host networking, and has the docker client of that host. The panel
is reached through the Caddy of the node (Host: panel.test). The fake Git server (gitapp/gitserver.py) is the Git
repository and also a fake Gitea API that keeps the statuses and comments of Polipan.
Usage: driver_m9.py setup | promote | preview
"""
import hashlib
import hmac
import http.client
import json
import re
import socket
import ssl
import subprocess
import sys
import time
import urllib.request

PANEL = "panel.test"
PASSWORD = "correct horse battery"
EMAIL = "owner@example.com"
STATE = "/e2e/m9-state.json"
GIT = "http://192.168.77.10:8000"
STAGING_HOST = "site.staging.test"
PROD_HOST = "site.prod.test"
PREVIEW_DOMAIN = "preview.test"


def fail(msg):
    print("FAIL:", msg, flush=True)
    sys.exit(1)


def ok(msg):
    print("ok:", msg, flush=True)


class Panel:
    def __init__(self):
        self.cookie, self.csrf = None, None

    def call(self, method, path, body=None, expect=None, headers=None):
        h = {"Host": PANEL, "Content-Type": "application/json"}
        h.update(headers or {})
        if self.cookie:
            h["Cookie"] = self.cookie
        if self.csrf and method != "GET":
            h["X-CSRF-Token"] = self.csrf
        for attempt in range(6):  # Caddy can reset a connection while it reloads its config
            try:
                conn = http.client.HTTPConnection("127.0.0.1", 80, timeout=60)
                raw_body = body if isinstance(body, bytes) else (json.dumps(body) if body is not None else None)
                conn.request(method, "/api/v1" + path, raw_body, h)
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
            fail(f"{method} {path}: want {expect}, got {resp.status}: {raw[:400]!r}")
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
        except (OSError, http.client.HTTPException) as e:  # the connection can fail while Caddy loads its config
            last = e
        if last and not isinstance(last, Exception):
            return last
        time.sleep(every)
    fail(f"timeout: {what} (last: {last})")


def docker(*args):
    r = subprocess.run(["docker", *args], capture_output=True, text=True, timeout=120)
    if r.returncode != 0:
        fail(f"docker {' '.join(args)}: {r.stderr.strip()}")
    return r.stdout.strip()


def dns_add(host):
    req = urllib.request.Request("http://127.0.0.1:8055/add-a", json.dumps({"host": host, "addresses": ["172.30.0.1"]}).encode(), method="POST")
    urllib.request.urlopen(req, timeout=10).read()


def trust_context():
    insecure = ssl.create_default_context()
    insecure.check_hostname = False
    insecure.verify_mode = ssl.CERT_NONE
    pem = urllib.request.urlopen("https://127.0.0.1:15000/roots/0", context=insecure, timeout=10).read()
    return ssl.create_default_context(cadata=pem.decode())


def https_get(host, ctx):
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
    return int(data.split(b" ", 2)[1]), data.decode("latin1")


def http_get(host, path="/"):
    conn = http.client.HTTPConnection("127.0.0.1", 80, timeout=15)
    conn.request("GET", path, headers={"Host": host})
    r = conn.getresponse()
    return r.status, r.getheader("Location"), r.read().decode("latin1")


def git_commit(version, branch="main"):
    req = urllib.request.Request(f"{GIT}/commit?version={version}&branch={branch}", b"", method="POST")
    return urllib.request.urlopen(req, timeout=60).read().decode()


def git_state():
    return json.load(urllib.request.urlopen(f"{GIT}/_test/state", timeout=10))


def load():
    return json.load(open(STATE))


def save(**kv):
    try:
        st = load()
    except FileNotFoundError:
        st = {}
    st.update(kv)
    json.dump(st, open(STATE, "w"))


def panel():
    p = Panel()
    p.login()
    return p


def deploy_live(p, svc_id, body=None, timeout=600):
    _, dep = p.call("POST", f"/services/{svc_id}/deploy", body or {}, expect=202)

    def live():
        _, d = p.call("GET", f"/deployments/{dep['id']}", expect=200)
        if d["state"] in ("failed", "cancelled", "rolled_back"):
            _, logs = p.call("GET", f"/deployments/{dep['id']}/logs", expect=200)
            print("\n".join(l["line"] for l in logs[-30:]))
            fail(f"deployment {d['state']}: {d.get('failure_reason')} {d.get('failure_detail')}")
        return d if d["state"] == "live" else None
    return wait(f"deployment {dep['id']} live", live, timeout=timeout, every=3)


def containers(service_id):
    return docker("ps", "-a", "-q", "--filter", f"label=polipan.service={service_id}").split()


def container_env(service_id):
    ids = docker("ps", "-q", "--filter", f"label=polipan.service={service_id}").split()
    if not ids:
        fail(f"no running container of service {service_id}")
    env = {}
    for line in docker("exec", ids[0], "env").splitlines():
        k, _, v = line.partition("=")
        env[k] = v
    return ids[0], env


# ---- setup ----

def setup():
    p = Panel()
    wait("panel behind caddy", lambda: http_get(PANEL, "/healthz")[0] == 200, timeout=180)
    token = re.search(r"token=([0-9a-f]+)", open("/e2e/setup-url").read()).group(1)
    p.call("POST", "/setup/first-user", {"setup_token": token, "email": EMAIL, "name": "Owner", "password": PASSWORD}, expect=201)
    p.login()
    # The fake Git server is on a private network: the org allows it. The preview domain is an org setting.
    p.call("PATCH", "/org", {"settings": {"allow_private_targets": True, "preview_domain": PREVIEW_DOMAIN}}, expect=200)
    _, conn = p.call("POST", "/git-connections", {"provider": "gitea", "name": "forge", "base_url": GIT, "token": "tok-e2e"}, expect=201)
    _, proj = p.call("POST", "/projects", {"name": "Shop"}, expect=201)
    _, envs = p.call("GET", f"/projects/{proj['id']}/environments", expect=200)
    staging = next(e for e in envs if e["kind"] == "staging")
    prod = next(e for e in envs if e["kind"] == "production")

    def site(env, host):
        _, svc = p.call("POST", f"/environments/{env['id']}/services", {
            "name": "site", "type": "app",
            "source": {"type": "git", "connection_id": conn["id"], "repo": "app", "branch": "main", "builder": "dockerfile", "auto_deploy": False},
            "config": {"port": 80}}, expect=201)
        p.call("POST", f"/services/{svc['id']}/domains", {"host": host, "container_port": 80, "https": False}, expect=201)
        return svc
    stg, prd = site(staging, STAGING_HOST), site(prod, PROD_HOST)
    # A volume in the base: a preview gets its own (AC-VOL-04).
    p.call("POST", f"/services/{stg['id']}/volumes", {"name": "data", "kind": "volume", "mount_path": "/data"}, expect=201)
    # The same names with other values in the two environments. The production secret is not a preview value.
    pid = proj["id"]
    p.call("POST", f"/projects/{pid}/secrets", {"name": "API_KEY", "value": "staging-key-111", "environment_id": staging["id"]}, expect=201)
    p.call("POST", f"/projects/{pid}/secrets", {"name": "API_KEY", "value": "production-key-999", "environment_id": prod["id"]}, expect=201)
    p.call("POST", f"/projects/{pid}/secrets", {"name": "PROD_ONLY_SECRET", "value": "prod-only-value-777"}, expect=201)
    p.call("POST", f"/projects/{pid}/variables", {"name": "GREETING", "value": "hello from a preview", "preview": True}, expect=201)
    save(project=pid, staging=staging["id"], production=prod["id"], stg_svc=stg["id"], prod_svc=prd["id"], conn=conn["id"], secret=conn["webhook_secret"])
    ok("setup: project, two environments, git connection, services with their own domains and secrets")
    print("SETUP PASSED")


# ---- T-E2E-05: promotion ----

def promote():
    st = load()
    p = panel()
    sha = git_commit(1)
    d = deploy_live(p, st["stg_svc"], {"commit": sha})
    wait("staging serves version 1", lambda: "version 1" in http_get(STAGING_HOST)[2], timeout=90)
    _, stg = p.call("GET", f"/services/{st['stg_svc']}", expect=200)
    rel_id = stg["live_release_id"]
    _, rel = p.call("GET", f"/releases/{rel_id}", expect=200)
    digest = rel["image_digest"]
    ok(f"staging is live with release {rel['number']}, digest {digest[:19]}")

    _, secrets_before = p.call("GET", f"/projects/{st['project']}/secrets?environment_id={st['production']}", expect=200)
    api_key_before = next(s for s in secrets_before if s["name"] == "API_KEY")
    status, _, _ = http_get(PROD_HOST)
    if status != 503:
        fail(f"production before the promotion: {status}")

    # AC-PROMO-04: a dry run shows the plan and changes nothing.
    _, plan = p.call("POST", f"/releases/{rel_id}/promote", {"target_environment_id": st["production"], "dry_run": True}, expect=200)
    item = plan["items"][0]
    if item["image_digest"] != digest or not plan["plan_hash"] or not item.get("changes"):
        fail(f"plan: {plan}")
    _, prod_rels = p.call("GET", f"/services/{st['prod_svc']}/releases", expect=200)
    if prod_rels["items"]:
        fail("a dry run made a release")
    ok("AC-PROMO-04: the dry run shows the diff and made no release")

    # The promotion with the plan hash.
    _, dep = p.call("POST", f"/releases/{rel_id}/promote", {"target_environment_id": st["production"], "plan_hash": plan["plan_hash"]}, expect=202)
    if dep["trigger"] != "promotion":
        fail(f"trigger: {dep['trigger']}")

    def live():
        _, x = p.call("GET", f"/deployments/{dep['id']}", expect=200)
        if x["state"] in ("failed", "cancelled", "rolled_back"):
            _, logs = p.call("GET", f"/deployments/{dep['id']}/logs", expect=200)
            print("\n".join(l["line"] for l in logs[-30:]))
            fail(f"promotion {x['state']}: {x.get('failure_reason')} {x.get('failure_detail')}")
        return x["state"] == "live"
    wait("promotion live", live, timeout=300, every=3)
    wait("production serves version 1", lambda: "version 1" in http_get(PROD_HOST)[2], timeout=90)

    # AC-PROMO-01: the same digest and no build.
    _, prod = p.call("GET", f"/services/{st['prod_svc']}", expect=200)
    _, prel = p.call("GET", f"/releases/{prod['live_release_id']}", expect=200)
    if prel["image_digest"] != digest or prel["source"].get("promoted_from_release_id") != rel_id:
        fail(f"production release: {prel}")
    _, logs = p.call("GET", f"/deployments/{dep['id']}/logs", expect=200)
    if any(l["stream"] == "build" for l in logs):
        fail("a build ran for the promotion")
    stg_c, prod_c = docker("inspect", "--format", "{{.Image}}", containers(st["stg_svc"])[0]), docker("inspect", "--format", "{{.Image}}", containers(st["prod_svc"])[0])
    if stg_c != prod_c:
        fail(f"the containers run different images: {stg_c} {prod_c}")
    ok("AC-PROMO-01: production runs the same image digest, no build ran")

    # AC-PROMO-02: the domains and the secrets of production are its own.
    _, doms = p.call("GET", f"/services/{st['prod_svc']}/domains", expect=200)
    if [x["host"] for x in doms] != [PROD_HOST]:
        fail(f"production domains: {doms}")
    _, secrets_after = p.call("GET", f"/projects/{st['project']}/secrets?environment_id={st['production']}", expect=200)
    api_key_after = next(s for s in secrets_after if s["name"] == "API_KEY")
    if api_key_after["current_version"] != api_key_before["current_version"] or api_key_after["id"] != api_key_before["id"]:
        fail("the production secret changed")
    _, env = container_env(st["prod_svc"])
    if env.get("API_KEY") != "production-key-999" or "staging-key-111" in json.dumps(env):
        fail(f"production container env: API_KEY={env.get('API_KEY')}")
    _, senv = container_env(st["stg_svc"])
    if senv.get("API_KEY") != "staging-key-111":
        fail("staging container lost its own secret")
    ok("AC-PROMO-02: production keeps its domain and its own secret, staging keeps its own")
    save(digest=digest)
    print("T-E2E-05 PASSED")


# ---- T-E2E-06: preview environments ----

def pr_hook(st, action, number, sha, branch, delivery):
    body = json.dumps({"action": action, "number": number, "repository": {"full_name": "app"}, "pull_request": {
        "number": number, "title": "Try it", "html_url": f"{GIT}/app/pulls/{number}", "merged": False, "user": {"login": "ann"},
        "head": {"sha": sha, "ref": branch, "repo": {"full_name": "app"}}, "base": {"ref": "main", "repo": {"full_name": "app"}}, "labels": []}}).encode()
    sig = hmac.new(st["secret"].encode(), body, hashlib.sha256).hexdigest()
    p = Panel()
    s, out = p.call("POST", f"/webhooks/git/{st['conn']}", body, headers={"X-Gitea-Event": "pull_request", "X-Gitea-Delivery": delivery, "X-Gitea-Signature": sig})
    if s != 202:
        fail(f"webhook {action}: {s} {out}")
    return out


def previews(p, st):
    _, items = p.call("GET", f"/projects/{st['project']}/previews", expect=200)
    return items


def preview():
    st = load()
    p = panel()
    host = f"pr-9-site.{PREVIEW_DOMAIN}"
    dns_add(host)  # the local ACME CA resolves the host to this node
    # The base environment has a live release, a volume and secrets that a preview must not get.
    _, stg = p.call("GET", f"/services/{st['stg_svc']}", expect=200)
    if not stg.get("live_release_id"):
        fail("run the promote step first")
    _, base_vols = p.call("GET", f"/services/{st['stg_svc']}/volumes", expect=200)
    base_vol_ids = {v["id"] for v in base_vols}
    p.call("PATCH", f"/projects/{st['project']}", {"settings": {"previews": {
        "enabled": True, "base_environment_id": st["staging"], "max_active": 5, "ttl_days": 7, "cleanup_grace_minutes": 0}}}, expect=200)

    sha_a = git_commit(11, "feature")
    pr_hook(st, "opened", 9, sha_a, "feature", "d-open")

    def active():
        items = previews(p, st)
        if items and items[0]["preview"]["state"] == "failed":
            fail(f"the preview failed: {items[0]['preview'].get('failure_reason')}")
        return items[0] if items and items[0]["preview"]["state"] == "active" and items[0]["preview"].get("url") else None
    env = wait("the preview is active", active, timeout=900, every=4)
    url_host = env["preview"]["url"].replace("https://", "")
    if env["slug"] != "pr-9" or url_host != host:
        fail(f"the preview: {env}")
    ctx = trust_context()

    def serves(version):
        def f():
            try:
                code, body = https_get(host, ctx)
            except (OSError, ssl.SSLError):
                return False
            return code == 200 and f"version {version}" in body
        return f
    wait("the preview URL serves the PR commit over HTTPS", serves(11), timeout=240, every=4)
    ok("AC-PREV-01: pr-9 exists and https://" + host + " serves the commit of the PR with a valid certificate")

    state = git_state()
    comments = [c for c in state["comments"] if c["number"] == 9]
    if len(comments) != 1 or host not in comments[0]["body"]:
        fail(f"the PR comment: {comments}")
    if not state["statuses"] or state["statuses"][0]["state"] != "pending" or state["statuses"][-1]["state"] != "success" or state["statuses"][-1]["sha"] != sha_a:
        fail(f"the commit statuses: {state['statuses']}")
    ok("AC-PREV-01: the PR has one comment with the URL and the statuses pending then success")

    # AC-PREV-04: the container has the preview values only.
    _, svcs = p.call("GET", f"/environments/{env['id']}/services", expect=200)
    site = next(s for s in svcs if s["name"] == "site")
    _, penv = container_env(site["id"])
    if penv.get("GREETING") != "hello from a preview":
        fail(f"preview variable missing: {list(penv)}")
    for name in ("PROD_ONLY_SECRET", "API_KEY"):
        if name in penv:
            fail(f"{name} is in the preview container")
    if "prod-only-value-777" in json.dumps(penv) or "production-key-999" in json.dumps(penv) or "staging-key-111" in json.dumps(penv):
        fail("a secret value of another environment is in the preview container")
    ok("AC-PREV-04: only preview values reach the preview container, no production secret")

    # AC-VOL-04: own volumes.
    _, pvols = p.call("GET", f"/services/{site['id']}/volumes", expect=200)
    if not pvols or any(v["id"] in base_vol_ids for v in pvols):
        fail(f"preview volumes: {pvols} base: {base_vol_ids}")
    vol_names = docker("volume", "ls", "-q").split()
    if not any(v["id"].lower() in n for v in pvols for n in vol_names):
        fail(f"the Docker volume of the preview is missing: {vol_names}")
    ok("AC-VOL-04: the preview has its own volume, none of the base")
    preview_services = [s["id"] for s in svcs]
    preview_volume_ids = [v["id"].lower() for v in pvols]

    # AC-PREV-02: a push builds the new commit; the same comment is updated.
    sha_b = git_commit(12, "feature")
    pr_hook(st, "synchronized", 9, sha_b, "feature", "d-push")
    wait("the preview URL serves the new commit", serves(12), timeout=600, every=4)
    state = git_state()
    comments = [c for c in state["comments"] if c["number"] == 9]
    if len(comments) != 1 or comments[0]["writes"] < 2 or sha_b[:7] not in comments[0]["body"]:
        fail(f"the comment after the push: {comments}")
    ok("AC-PREV-02: the URL serves the new commit and the same comment was updated")

    # AC-PREV-03: PR closed (grace 0): the environment, containers and volumes are gone.
    pr_hook(st, "closed", 9, sha_b, "feature", "d-close")
    wait("the preview is deleted", lambda: previews(p, st) == [], timeout=180, every=4)
    s, _ = p.call("GET", f"/environments/{env['id']}")
    if s != 404:
        fail(f"the environment still exists: {s}")
    left = [c for sid in preview_services for c in containers(sid)]
    if left:
        fail(f"containers are left: {left}")
    vols = docker("volume", "ls", "-q").split()
    if any(v in n for v in preview_volume_ids for n in vols):
        fail(f"volumes are left: {vols}")
    # The base environment is untouched.
    _, stg_now = p.call("GET", f"/services/{st['stg_svc']}", expect=200)
    if stg_now["live_release_id"] != stg["live_release_id"] or not containers(st["stg_svc"]):
        fail("the base environment changed")
    ok("AC-PREV-03: the closed PR left no environment, container or volume")
    print("T-E2E-06 PASSED")


if __name__ == "__main__":
    {"setup": setup, "promote": promote, "preview": preview}[sys.argv[1]]()
