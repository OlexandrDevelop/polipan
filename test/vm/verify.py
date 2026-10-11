# SPDX-License-Identifier: Apache-2.0
"""Checks of Polipan in the test VM (see test/vm/README.md). Run it with: test/vm/vm.sh verify [stage ...]

Stages, in this order: setup deploy bluegreen rollback stopstart restart-server restart-agent reboot upgrade https
With no stage name, all stages run. The panel is reached on the forwarded port of this computer. The traffic
generator runs inside the VM (load.py), so the numbers are not changed by the user-mode network of QEMU.
Every check prints PASS or FAIL with a short text. The exit code is 1 when a check failed.
"""
import calendar
import http.client
import json
import os
import re
import subprocess
import sys
import time

HTTP = int(os.environ.get("POLIPAN_VM_HTTP_PORT", "8088"))
HTTPS = int(os.environ.get("POLIPAN_VM_HTTPS_PORT", "8443"))
VM_SH = os.environ["POLIPAN_VM_SH"]
STATE = os.path.join(os.path.dirname(os.environ["POLIPAN_VM_KEY"]), "verify-state.json")
ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
HERE = os.path.dirname(os.path.abspath(__file__))

EMAIL = "owner@example.com"
PASSWORD = "correct horse battery"
APP = "app.test"
ZD = "zd.test"
SECURE = "secure.test"

results = []


def check(name, ok, detail=""):
    results.append((name, ok, detail))
    print(("PASS" if ok else "FAIL"), name + (": " + detail if detail else ""), flush=True)
    if not ok:
        raise Failed(name)


class Failed(Exception):
    pass


def vm(cmd, check_rc=True, timeout=600, stdin=None):
    """Runs a shell command in the VM. Returns the output."""
    r = subprocess.run([VM_SH, "ssh", cmd], capture_output=True, text=True, timeout=timeout, input=stdin)
    if check_rc and r.returncode != 0:
        raise RuntimeError(f"vm command failed ({r.returncode}): {cmd}\n{r.stdout}\n{r.stderr}")
    return (r.stdout + r.stderr).strip() if not check_rc else r.stdout.strip()


def compose(args):
    """Runs docker compose in /opt/polipan (only root can enter it)."""
    return vm("sudo sh -c 'cd /opt/polipan && docker compose %s'" % args)


def wait(what, fn, timeout=120, every=2):
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
    raise Failed(f"timeout: {what} (last: {last})")


def load_state():
    try:
        return json.load(open(STATE))
    except OSError:
        return {}


def save_state(st):
    json.dump(st, open(STATE, "w"))


class Panel:
    def __init__(self):
        self.cookie = None
        self.csrf = None

    def call(self, method, path, body=None, expect=None):
        headers = {"Content-Type": "application/json"}
        if self.cookie:
            headers["Cookie"] = self.cookie
        if self.csrf and method != "GET":
            headers["X-CSRF-Token"] = self.csrf
        for attempt in range(6):
            try:
                conn = http.client.HTTPConnection("127.0.0.1", HTTP, timeout=30)
                conn.request(method, "/api/v1" + path, json.dumps(body) if body is not None else None, headers)
                resp = conn.getresponse()
                raw = resp.read()
                break
            except (ConnectionError, http.client.HTTPException):
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
            raise Failed(f"{method} {path}: want {expect}, got {resp.status}: {raw[:300]!r}")
        return resp.status, data

    def login(self):
        self.call("POST", "/auth/login", {"email": EMAIL, "password": PASSWORD}, expect=200)
        self.call("GET", "/me", expect=200)


def get(host, path="/", port=HTTP):
    """GET through the Caddy of the VM (the forwarded port), with a Host header. Returns (status, body)."""
    conn = http.client.HTTPConnection("127.0.0.1", port, timeout=15)
    conn.request("GET", path, headers={"Host": host})
    r = conn.getresponse()
    return r.status, r.read().decode("latin1")


def panel_ready(timeout=300):
    wait("panel answers /healthz", lambda: get(f"127.0.0.1:{HTTP}", "/healthz")[0] == 200, timeout=timeout, every=3)


def start_load(host, name="load"):
    vm("sudo rm -f /tmp/%s.log /tmp/%s.log.stop" % (name, name))
    subprocess.run([VM_SH, "ssh", "cat > /tmp/load.py"], input=open(os.path.join(HERE, "load.py")).read(), text=True, check=True)
    vm(f"nohup python3 /tmp/load.py {host} /tmp/{name}.log 20 >/dev/null 2>&1 &")
    time.sleep(2)


def stop_load(name="load"):
    """Stops the traffic generator. Returns a list of (time, status, body)."""
    vm(f"touch /tmp/{name}.log.stop")
    time.sleep(4)
    out = vm(f"cat /tmp/{name}.log")
    log = []
    for line in out.splitlines():
        t, s, b = line.split(" ", 2)
        log.append((float(t), int(s), b))
    return log


def no_failures(log, what):
    bad = [r for r in log if r[1] != 200]
    check(what, not bad and len(log) > 0, f"{len(log)} requests, {len(bad)} failed" + (f", first: {bad[0]}" if bad else ""))


def first_seen(log, version):
    ts = [t for t, s, b in log if s == 200 and b == f"version {version}"]
    return min(ts) if ts else None


def node_ready(p):
    _, nodes = p.call("GET", "/nodes", expect=200)
    return bool(nodes) and nodes[0]["state"] == "ready"


def heartbeat_after(p, since):
    _, nodes = p.call("GET", "/nodes", expect=200)
    if not nodes or nodes[0]["state"] != "ready":
        return False
    beat = calendar.timegm(time.strptime(nodes[0]["last_heartbeat_at"][:19], "%Y-%m-%dT%H:%M:%S"))
    return beat >= since


def deploy_wait(p, svc_id, state="live", timeout=300):
    _, dep = p.call("POST", f"/services/{svc_id}/deploy", {}, expect=202)

    def done():
        _, d = p.call("GET", f"/deployments/{dep['id']}", expect=200)
        return d if d["state"] in ("live", "failed", "rolled_back", "cancelled") else None
    d = wait("deployment ends", done, timeout=timeout, every=1)
    if d["state"] != state:
        raise Failed(f"deployment {d['state']} ({d.get('failure_reason')}: {d.get('failure_detail')}), want {state}")
    return d


def set_vars(p, proj_id, svc_id, values):
    _, existing = p.call("GET", f"/projects/{proj_id}/variables?service_id={svc_id}", expect=200)
    by_name = {v["name"]: v for v in existing}
    for name, value in values.items():
        if name in by_name:
            p.call("PATCH", f"/variables/{by_name[name]['id']}", {"name": name, "value": value, "service_id": svc_id}, expect=200)
        else:
            p.call("POST", f"/projects/{proj_id}/variables", {"name": name, "value": value, "service_id": svc_id}, expect=201)


def logged_in():
    p = Panel()
    p.login()
    return p


# ---- stages ----

def stage_setup():
    panel_ready()
    url = compose("exec -T server /polipan-server setup-url").strip()
    check("setup link uses the forwarded address", url.startswith(f"http://127.0.0.1:{HTTP}/setup?token="), url)
    token = re.search(r"token=([0-9a-f]+)", url).group(1)
    p = Panel()
    s, _ = p.call("POST", "/setup/first-user", {"setup_token": "wrong", "email": EMAIL, "name": "Owner", "password": PASSWORD})
    check("first user: a wrong setup token is refused", s != 201, f"status {s}")
    p.call("POST", "/setup/first-user", {"setup_token": token, "email": EMAIL, "name": "Owner", "password": PASSWORD}, expect=201)
    s, _ = p.call("POST", "/setup/first-user", {"setup_token": token, "email": "x@example.com", "name": "X", "password": PASSWORD})
    check("first user: created with the setup link, the link works once", s == 404, f"second use gave {s}")
    s, _ = Panel().call("POST", "/auth/login", {"email": EMAIL, "password": "wrong password!!"})
    check("login: a wrong password gives 401", s == 401, f"status {s}")
    p.login()
    check("login: the first user logs in", True)
    wait("node ready", lambda: node_ready(p), timeout=120, every=3)
    check("the node of this VM is ready", True)


def stage_deploy():
    p = logged_in()
    _, proj = p.call("POST", "/projects", {"name": "VM check"}, expect=201)
    _, envs = p.call("GET", f"/projects/{proj['id']}/environments", expect=200)
    env = next(e for e in envs if e["kind"] == "production")
    cfg = {"image": "nginx:alpine", "port": 80}
    _, svc = p.call("POST", f"/environments/{env['id']}/services", {"name": "web", "type": "app", "source": {"image": "nginx:alpine"}, "config": cfg}, expect=201)
    t0 = time.time()
    d = deploy_wait(p, svc["id"], timeout=300)
    check("image service (nginx:alpine) deployed and live", True, f"{time.time() - t0:.0f} s including the image pull, strategy {d['strategy']}")
    p.call("POST", f"/services/{svc['id']}/domains", {"host": APP, "container_port": 80, "https": False}, expect=201)
    wait("app answers", lambda: get(APP)[0] == 200 and "nginx" in get(APP)[1].lower(), timeout=60)
    check(f"the app answers on the forwarded port with Host {APP}", True)
    st = load_state()
    st.update(project_id=proj["id"], env_id=env["id"], web_id=svc["id"])
    save_state(st)


GIT_REPO = "https://github.com/docker/welcome-to-docker"


def stage_gitbuild():
    """A build from a public Git repository with the rootless BuildKit (Ubuntu 24.04 limits user namespaces)."""
    st = load_state()
    p = logged_in()
    out = vm("sudo docker ps --filter name=polipan-buildkitd-1 --format '{{.Status}}'")
    check("BuildKit runs and does not restart", out.startswith("Up"), out.strip())
    out = vm("sudo docker inspect -f '{{.HostConfig.Privileged}}' polipan-buildkitd-1")
    check("BuildKit is not privileged", out.strip() == "false", out.strip())
    out = vm("sysctl -n kernel.apparmor_restrict_unprivileged_userns || echo 0")
    check("the system-wide limit on user namespaces is not turned off by the installer", out.strip() == "1", "value " + out.strip())
    _, svc = p.call("POST", f"/environments/{st['env_id']}/services", {
        "name": "gitbuild", "type": "app",
        "source": {"type": "git", "repo": GIT_REPO, "branch": "main", "builder": "dockerfile", "provider": "github"},
        "config": {"port": 3000}}, expect=201)
    t0 = time.time()
    deploy_wait(p, svc["id"], timeout=900)
    check("a service built from Git deployed and live", True, f"{time.time() - t0:.0f} s")
    p.call("POST", f"/services/{svc['id']}/domains", {"host": "git.test", "container_port": 3000, "https": False}, expect=201)
    wait("git app answers", lambda: get("git.test")[0] == 200, timeout=60)
    check("the app built from Git answers", True)
    st["git_id"] = svc["id"]
    save_state(st)


ZD_SCRIPT = r"""
import os, time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
V = os.environ.get("VERSION", "0")
class H(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    def log_message(self, *a): pass
    def reply(self, code, body):
        b = body.encode()
        self.send_response(code); self.send_header("Content-Length", str(len(b))); self.end_headers(); self.wfile.write(b)
    def do_GET(self):
        p = self.path.split("?")[0]
        if p == "/ready": self.reply(200, "ready")
        else: self.reply(200, "version " + V)
ThreadingHTTPServer(("0.0.0.0", 8000), H).serve_forever()
"""


def stage_bluegreen():
    p = logged_in()
    st = load_state()
    cfg = {"image": "python:3.12-slim", "port": 8000, "command": ["python", "-u", "-c", ZD_SCRIPT],
           "health": {"type": "http", "path": "/ready", "interval": 1, "timeout": 2, "healthy_threshold": 3, "unhealthy_threshold": 3,
                      "start_period": 5, "deploy_timeout": 60},
           "strategy_options": {"drain_seconds": 5}}
    _, svc = p.call("POST", f"/environments/{st['env_id']}/services", {"name": "zd", "type": "app", "source": {"image": cfg["image"]}, "config": cfg}, expect=201)
    p.call("POST", f"/services/{svc['id']}/domains", {"host": ZD, "container_port": 8000, "https": False}, expect=201)
    set_vars(p, st["project_id"], svc["id"], {"VERSION": "1"})
    d = deploy_wait(p, svc["id"], timeout=300)
    check("blue-green service: first deploy is live", d["strategy"] == "blue_green", f"strategy {d['strategy']}")
    wait("version 1 answers", lambda: get(ZD)[1] == "version 1", timeout=30)
    st.update(zd_id=svc["id"])
    save_state(st)

    start_load(ZD)
    times = {}
    for v in (2, 3, 4):
        set_vars(p, st["project_id"], svc["id"], {"VERSION": str(v)})
        d = deploy_wait(p, svc["id"])
        check(f"update to version {v} is blue-green", d["strategy"] == "blue_green", d["strategy"])
        wait(f"version {v} answers", lambda: get(ZD)[1] == f"version {v}", timeout=30, every=0.5)
    log = stop_load()
    no_failures(log, "blue-green: 3 updates under 20 requests/s, no failed request")
    firsts = [first_seen(log, v) for v in (2, 3, 4)]
    clean = all(f is not None for f in firsts)
    for old, new in ((1, 2), (2, 3), (3, 4)):
        f = first_seen(log, new)
        late = [r for r in log if r[2] == f"version {old}" and f is not None and r[0] > f + 0.3]
        clean = clean and not late
    check("blue-green: the answer changed from old to new at one moment each time", clean)


def stage_rollback():
    p = logged_in()
    st = load_state()
    _, rels = p.call("GET", f"/services/{st['zd_id']}/releases", expect=200)
    items = rels["items"]
    v4, v3 = items[0], items[1]
    check("rollback: the previous release kept its containers", v3["status"] == "previous" and v3["fast_rollback_possible"], f"{v3['status']} {v3['fast_rollback_possible']}")
    start_load(ZD, "rb")
    t0 = time.time()
    _, dep = p.call("POST", f"/services/{st['zd_id']}/rollback", {"release_id": v3["id"]}, expect=202)
    wait("traffic on version 3", lambda: get(ZD)[1] == "version 3", timeout=60, every=0.2)
    took = time.time() - t0
    d = wait("rollback live", lambda: (lambda x: x if x["state"] == "live" else None)(p.call("GET", f"/deployments/{dep['id']}", expect=200)[1]), timeout=120)
    log = stop_load("rb")
    check("rollback: one click, traffic is back on version 3", took < 10 and d["trigger"] == "rollback", f"{took:.1f} s")
    no_failures(log, "rollback: no failed request while traffic ran")


def stage_stopstart():
    p = logged_in()
    st = load_state()
    p.call("POST", f"/services/{st['web_id']}/stop", {}, expect=202)
    wait("503 after stop", lambda: get(APP)[0] == 503, timeout=60, every=1)
    check("stop: the app answers 503", True)
    _, sv = p.call("GET", f"/services/{st['web_id']}", expect=200)
    check("stop: the release stays", bool(sv.get("live_release_id")))
    p.call("POST", f"/services/{st['web_id']}/start", {}, expect=202)
    wait("200 after start", lambda: get(APP)[0] == 200, timeout=90, every=1)
    check("start: the app answers 200 again", True)


def app_serves():
    return get(APP)[0] == 200 and get(ZD)[0] == 200


def stage_restart_server():
    p = logged_in()
    start_load(APP, "rs")
    t0 = time.time()
    compose("kill server && docker compose start server")
    wait("server healthy", lambda: get(f"127.0.0.1:{HTTP}", "/healthz")[0] == 200, timeout=120, every=2)
    back = time.time() - t0
    p2 = Panel()
    p2.login()
    check("server restart: the panel is back and the login works", True, f"{back:.0f} s")
    wait("node ready", lambda: heartbeat_after(p2, t0), timeout=120, every=3)
    check("server restart: the agent reconnected, the node is ready", True)
    log = stop_load("rs")
    no_failures(log, "server restart: the app served all requests during the restart")


def stage_restart_agent():
    p = logged_in()
    start_load(APP, "ra")
    pid0 = vm("systemctl show -p MainPID --value polipan-agent")
    t0 = time.time()
    vm("sudo systemctl restart polipan-agent")
    pid1 = vm("systemctl show -p MainPID --value polipan-agent")
    check("agent restart: a new process runs", pid0 != pid1 and pid1 != "0", f"pid {pid0} -> {pid1}")
    wait("node ready after the restart", lambda: heartbeat_after(p, t0), timeout=120, every=3)
    check("agent restart: the node is ready again", True)
    log = stop_load("ra")
    no_failures(log, "agent restart: the app served all requests")
    wait("both apps serve", app_serves, timeout=30)


def stage_reboot():
    p = logged_in()
    boot0 = vm("cat /proc/sys/kernel/random/boot_id")
    _, svcs = p.call("GET", f"/environments/{load_state()['env_id']}/services", expect=200)
    before = sorted((s["name"], s.get("desired_state")) for s in svcs)
    t0 = time.time()
    subprocess.run([VM_SH, "ssh", "sudo systemctl reboot"], capture_output=True, text=True, timeout=60)
    # SSH goes away, then comes back with a new boot id.
    def rebooted():
        r = subprocess.run([VM_SH, "ssh", "cat /proc/sys/kernel/random/boot_id"], capture_output=True, text=True, timeout=30)
        return r.returncode == 0 and r.stdout.strip() not in ("", boot0)
    wait("the VM boots again", rebooted, timeout=300, every=3)
    check("reboot: the VM started again with a new boot", True, f"SSH back after {time.time() - t0:.0f} s")
    panel_ready(timeout=300)
    check("reboot: the panel answers by itself", True, f"{time.time() - t0:.0f} s after the reboot command")
    p = Panel()
    wait("login after reboot", lambda: (p.login() or True), timeout=60, every=3)
    check("reboot: the login works (data kept)", True)
    wait("apps serve after reboot", app_serves, timeout=300, every=3)
    check("reboot: both apps serve again without any action", True, f"{time.time() - t0:.0f} s after the reboot command")
    wait("node ready", lambda: node_ready(p), timeout=120, every=3)
    check("reboot: the node is ready", True)
    _, svcs = p.call("GET", f"/environments/{load_state()['env_id']}/services", expect=200)
    after = sorted((s["name"], s.get("desired_state")) for s in svcs)
    check("reboot: the services have the same desired state", before == after, f"{after}")
    en = vm("systemctl is-enabled docker polipan-agent polipan-build-firewall | tr '\\n' ' '")
    check("reboot: docker, agent and firewall start at boot (enabled)", en.split() == ["enabled"] * 3, en)


def stage_upgrade():
    """Builds a new version, copies it to the VM and runs install.sh again."""
    p = logged_in()
    agent_v0 = vm("/usr/local/bin/polipan-agent version")
    pid0 = vm("systemctl show -p MainPID --value polipan-agent")
    sum0 = vm("sha256sum /usr/local/bin/polipan-agent | cut -d ' ' -f 1")
    server_id0 = compose("ps -q server")
    ver = "vm-upgrade-" + str(int(time.time()))
    commit = subprocess.run(["git", "-C", ROOT, "rev-parse", "--short", "HEAD"], capture_output=True, text=True).stdout.strip()
    for name in ("server", "agent"):
        subprocess.run(["docker", "build", "-q", "-f", f"{ROOT}/deploy/docker/{name}.Dockerfile", "--build-arg", f"VERSION={ver}",
                        "--build-arg", f"COMMIT={commit}", "-t", f"polipan-{name}:dev", ROOT], check=True, capture_output=True)
    start_load(APP, "up")
    t0 = time.time()
    env = dict(os.environ, SKIP_BUILD="1")
    r = subprocess.run([VM_SH, "install"], capture_output=True, text=True, env=env, timeout=1200)
    check("upgrade: install.sh ran again on the VM", r.returncode == 0, f"{time.time() - t0:.0f} s" + ("" if r.returncode == 0 else r.stdout[-600:] + r.stderr[-600:]))
    log = stop_load("up")
    agent_v1 = vm("/usr/local/bin/polipan-agent version")
    pid1 = vm("systemctl show -p MainPID --value polipan-agent")
    sum1 = vm("sha256sum /usr/local/bin/polipan-agent | cut -d ' ' -f 1")
    check("upgrade: the agent binary was replaced", sum0 != sum1 and ver in agent_v1, f"{agent_v0!r} -> {agent_v1!r}")
    check("upgrade: the running agent was restarted (new process)", pid0 != pid1 and pid1 != "0", f"pid {pid0} -> {pid1}")
    server_id1 = compose("ps -q server")
    check("upgrade: the server container uses the new image", server_id0 != server_id1)
    panel_ready()
    p = Panel()
    p.login()
    check("upgrade: the user and the login are kept", True)
    wait("node ready", lambda: heartbeat_after(p, t0), timeout=120, every=3)
    check("upgrade: the node is ready", True)
    wait("apps serve", app_serves, timeout=60)
    check("upgrade: the apps still serve", True)
    no_failures(log, "upgrade: the app served all requests during the upgrade")


def stage_https():
    """HTTPS with a local test ACME server (Pebble). A real domain and a real certificate are not possible here."""
    p = logged_in()
    st = load_state()
    subprocess.run([VM_SH, "ssh", "mkdir -p /tmp/pebble && cat > /tmp/pebble/pebble.json"], input=open(f"{ROOT}/test/e2e/pebble.json").read(), text=True, check=True)
    vm("""sudo sh -s <<'EOS'
set -e
docker rm -f pebble challtest >/dev/null 2>&1 || true
docker network inspect acmetest >/dev/null 2>&1 || docker network create acmetest >/dev/null
# The firewall of the agent stops containers of the app network from talking to each other. So the test DNS is on its
# own network, and Pebble is on both networks (the app network, where Caddy is, and this one).
docker run -d --name challtest --network acmetest --network-alias challtest -p 127.0.0.1:8055:8055 \
  ghcr.io/letsencrypt/pebble-challtestsrv:latest -http01 "" -https01 "" -tlsalpn01 "" -defaultIPv6 "" >/dev/null
docker run -d --name pebble --network polipan --network-alias pebble -p 127.0.0.1:15000:15000 \
  -e PEBBLE_VA_NOSLEEP=1 -v /tmp/pebble/pebble.json:/test/config/pebble-config.json \
  ghcr.io/letsencrypt/pebble:latest -config /test/config/pebble-config.json -dnsserver challtest:8053 >/dev/null
docker network connect acmetest pebble
docker cp pebble:/test/certs/pebble.minica.pem /etc/polipan/pebble.minica.pem
sed -i '/ACME/d' /etc/polipan/agent.env
printf 'POLIPAN_AGENT_ACME_CA_URL=https://pebble:14000/dir\\nPOLIPAN_AGENT_ACME_TRUSTED_ROOTS=/etc/polipan/pebble.minica.pem\\n' >> /etc/polipan/agent.env
grep -q ' secure.test' /etc/hosts || echo '127.0.0.1 secure.test' >> /etc/hosts
# Every Pebble start is a new CA: Caddy must forget the account and certificates of the old one.
docker exec polipan-caddy sh -c 'rm -rf /data/acme/pebble-14000-dir /data/caddy/certificates/pebble-14000-dir' || true
docker rm -f polipan-caddy >/dev/null
systemctl restart polipan-agent
EOS""")
    # The test name resolves to the host address on the Docker network of the node (where Caddy listens).
    wait("challtestsrv up", lambda: vm("curl -fsS -X POST -d '{\"host\":\"secure.test\",\"addresses\":[\"10.210.1.1\"]}' http://127.0.0.1:8055/add-a; echo ok", check_rc=False).endswith("ok"), timeout=60, every=2)
    check("Pebble (local test ACME server) and its test DNS are running in the VM", True)
    wait("node ready", lambda: node_ready(p), timeout=120, every=3)
    _, old = p.call("GET", f"/services/{st['web_id']}/domains", expect=200)
    for d in old:
        if d["host"] == SECURE:
            p.call("DELETE", f"/domains/{d['id']}", expect=204)
    _, dom = p.call("POST", f"/services/{st['web_id']}/domains", {"host": SECURE, "container_port": 80}, expect=201)

    def issued():
        _, ds = p.call("GET", f"/services/{st['web_id']}/domains", expect=200)
        d = next(d for d in ds if d["host"] == SECURE)
        if d["tls_status"] == "failed":
            raise Failed(f"tls failed: {d.get('tls_error')}")
        return d["tls_status"] == "issued"
    wait("certificate issued", issued, timeout=240, every=3)
    check("HTTPS: the domain shows tls_status issued (certificate from Pebble)", True)
    root = vm("curl -fsS -k https://127.0.0.1:15000/roots/0")
    open("/tmp/pebble-root.pem", "w").write(root + "\n")
    subprocess.run([VM_SH, "ssh", "cat > /tmp/pebble-root.pem"], input=root + "\n", text=True, check=True)
    out = vm("curl -sS --cacert /tmp/pebble-root.pem -o /dev/null -w '%{http_code} %{ssl_verify_result} %{scheme}' https://secure.test/")
    check("HTTPS in the VM: https://secure.test answers 200 and the certificate is trusted (via /etc/hosts)", out.split()[:2] == ["200", "0"], out)
    out = vm("curl -sS -o /dev/null -w '%{http_code} %{redirect_url}' http://secure.test/")
    check("HTTPS: plain HTTP redirects to https", out.startswith("30") and "https://secure.test" in out, out)
    # From this computer, through the forwarded port 8443 with the name in the TLS handshake.
    import socket
    import ssl
    ctx = ssl.create_default_context(cadata=root)
    with socket.create_connection(("127.0.0.1", HTTPS), timeout=15) as s, ctx.wrap_socket(s, server_hostname=SECURE) as tls:
        tls.sendall(f"GET / HTTP/1.1\r\nHost: {SECURE}\r\nConnection: close\r\n\r\n".encode())
        data = b""
        while True:
            c = tls.recv(65536)
            if not c:
                break
            data += c
        issuer = tls.getpeercert().get("issuer")
    check("HTTPS from this computer through the forwarded port 8443 (certificate checked against the Pebble root)", data.startswith(b"HTTP/1.1 200"), f"issuer {issuer}")
    # Put the VM back to the plain setup.
    vm("sudo sh -c \"sed -i '/ACME/d' /etc/polipan/agent.env; printf 'POLIPAN_AGENT_ACME_CA_URL=\\nPOLIPAN_AGENT_ACME_TRUSTED_ROOTS=\\n' >> /etc/polipan/agent.env; sed -i '/secure.test/d' /etc/hosts; docker rm -f pebble challtest >/dev/null; docker network rm acmetest >/dev/null; rm -f /etc/polipan/pebble.minica.pem; docker exec polipan-caddy sh -c 'rm -rf /data/acme/pebble-14000-dir /data/caddy/certificates/pebble-14000-dir' || true; docker rm -f polipan-caddy >/dev/null; systemctl restart polipan-agent\"")


STAGES = [("setup", stage_setup), ("deploy", stage_deploy), ("gitbuild", stage_gitbuild), ("bluegreen", stage_bluegreen), ("rollback", stage_rollback),
          ("stopstart", stage_stopstart), ("restart-server", stage_restart_server), ("restart-agent", stage_restart_agent),
          ("reboot", stage_reboot), ("upgrade", stage_upgrade), ("https", stage_https)]


def main():
    names = sys.argv[1:] or [n for n, _ in STAGES]
    known = dict(STAGES)
    for n in names:
        if n not in known:
            sys.exit(f"unknown stage {n}. Stages: {' '.join(known)}")
    failed = False
    for n in names:
        print(f"\n== {n}", flush=True)
        try:
            known[n]()
        except Failed as e:
            print("FAIL (stop):", e)
            failed = True
            break
        except Exception as e:  # a crash is a failure, not a pass
            print("FAIL (error):", repr(e))
            failed = True
            break
    ok = sum(1 for r in results if r[1])
    print(f"\n{ok} checks passed, {len(results) - ok} failed" + (", stopped at the first failure" if failed else ""))
    sys.exit(1 if failed else 0)


main()
