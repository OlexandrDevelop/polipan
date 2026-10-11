# SPDX-License-Identifier: Apache-2.0
"""End-to-end driver of milestone M7 (run-m7.sh): T-E2E-07, T-E2E-08, AC-NODE-01, 03 to 10, REQ-SEC-106.

It runs on the host. The three nodes are Docker-in-Docker containers. The panel is reached through the Caddy of
node 1 (Host: panel.test), like a user would. The state of the nodes is read with `docker exec`.
Usage: driver_m7.py setup SETUP_URL_FILE | wait-ready NODE_NAME SECONDS | scenario N1 N2 N3 IP1 IP2 IP3
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
IP1 = "172.29.7.11"
NODE_DOWN_AFTER = 30  # the server of run-m7.sh runs with POLIPAN_NODE_DOWN_AFTER=30s
FAILOVER_BUDGET = NODE_DOWN_AFTER + 60  # AC-NODE-04: node_down_after + 60 s


def fail(msg):
    print("FAIL:", msg, flush=True)
    sys.exit(1)


def ok(msg):
    print("ok:", msg, flush=True)


def run(*cmd, check=True, timeout=120):
    r = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout)
    if check and r.returncode != 0:
        fail(f"{' '.join(cmd)}: {r.stderr.strip() or r.stdout.strip()}")
    return r.stdout.strip() if r.returncode == 0 else ""


def dx(node, *cmd, check=True, timeout=120):
    return run("docker", "exec", node, *cmd, check=check, timeout=timeout)


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
    def __init__(self, ip=IP1):
        self.ip, self.cookie, self.csrf = ip, None, None

    def call(self, method, path, body=None, expect=None):
        headers = {"Host": PANEL, "Content-Type": "application/json"}
        if self.cookie:
            headers["Cookie"] = self.cookie
        if self.csrf and method != "GET":
            headers["X-CSRF-Token"] = self.csrf
        for attempt in range(6):  # Caddy can reset a connection while it loads its config
            try:
                conn = http.client.HTTPConnection(self.ip, 80, timeout=30)
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
            fail(f"{method} {path}: want {expect}, got {resp.status}: {raw[:400]!r}")
        return resp.status, data

    def login(self):
        self.call("POST", "/auth/login", {"email": EMAIL, "password": PASSWORD}, expect=200)
        self.call("GET", "/me", expect=200)


def http_get(ip, host, path="/", timeout=3):
    """GET through the Caddy of a node. Returns (status, body), or (0, '') when the node does not answer."""
    try:
        conn = http.client.HTTPConnection(ip, 80, timeout=timeout)
        conn.request("GET", path, headers={"Host": host})
        r = conn.getresponse()
        return r.status, r.read().decode("latin1").strip()
    except (OSError, http.client.HTTPException):
        return 0, ""


def logged_in():
    p = Panel()
    p.login()
    return p


# ---- setup and joins ----

def setup(url_file):
    p = Panel()
    wait("the panel answers through Caddy", lambda: http_get(IP1, PANEL, "/healthz", timeout=5)[0] == 200, timeout=180)
    token = re.search(r"token=([0-9a-f]+)", open(url_file).read()).group(1)
    p.call("POST", "/setup/first-user", {"setup_token": token, "email": EMAIL, "name": "Owner", "password": PASSWORD}, expect=201)
    p.login()
    ok("the first user exists and the panel answers through the Caddy of node 1")


def node_by_name(p, name):
    _, nodes = p.call("GET", "/nodes", expect=200)
    return next((n for n in nodes if n["name"] == name), None)


def wait_ready(name, seconds):
    p = logged_in()

    def ready():
        n = node_by_name(p, name)
        return n if n and n["state"] == "ready" and n.get("mesh_ip") else None
    n = wait(f"node {name} is ready with a mesh address", ready, timeout=int(seconds), every=1)
    ok(f"AC-NODE-01: node {name} is ready, mesh address {n['mesh_ip']}, subnet {n['mesh_subnet']}")


# ---- helpers of the scenario ----

def deploy_wait(p, svc_id, timeout=300, expect="live"):
    _, dep = p.call("POST", f"/services/{svc_id}/deploy", {}, expect=202)
    return dep_wait(p, dep["id"], timeout, expect)


def dep_wait(p, dep_id, timeout=300, expect="live"):
    def done():
        _, d = p.call("GET", f"/deployments/{dep_id}", expect=200)
        return d if d["state"] in ("live", "failed", "rolled_back", "cancelled") else None
    d = wait(f"deployment {dep_id} ends", done, timeout=timeout, every=1)
    if d["state"] != expect:
        fail(f"deployment {dep_id} is {d['state']} ({d.get('failure_reason')}: {d.get('failure_detail')}), want {expect}")
    return d


def alias_of(svc_id):
    return "svc-" + svc_id.removeprefix("svc_").lower().replace("_", "-")


def create_service(p, env_id, name, image, config, failover=None, domain=None, volume=None):
    cfg = {"image": image, "health": {"type": "tcp", "port": config.get("port", 8080)},
           "strategy_options": {"drain_seconds": 2}}
    cfg.update(config)
    _, svc = p.call("POST", f"/environments/{env_id}/services", {"name": name, "type": "app", "source": {"image": image}, "config": cfg}, expect=201)
    if failover:
        p.call("PATCH", f"/services/{svc['id']}", {"failover": failover}, expect=200)
    if domain:
        p.call("POST", f"/services/{svc['id']}/domains", {"host": domain, "container_port": cfg["port"], "https": False}, expect=201)
    if volume:
        _, vol = p.call("POST", f"/services/{svc['id']}/volumes", {"name": "data", "mount_path": volume}, expect=201)
        svc["volume"] = vol
    return svc


def httpd(extra=""):
    # nginx on port 8080. The page says on which node the container runs (POLIPAN_NODE is set by the agent).
    return ["sh", "-c", "mkdir -p /www && echo hello-$POLIPAN_NODE > /www/index.html && " + extra +
            "printf 'server { listen 8080; root /www; }' > /etc/nginx/conf.d/default.conf && exec nginx -g 'daemon off;'"]


def containers(node, svc_id, all_states=False):
    flags = ["-a"] if all_states else []
    out = dx(node, "docker", "ps", *flags, "--filter", f"label=polipan.service={svc_id}", "--format", "{{.Names}}", check=False)
    return [x for x in out.split("\n") if x]


def container_ip(node, name):
    return dx(node, "docker", "inspect", "-f", "{{.NetworkSettings.Networks.polipan.IPAddress}}", name)


def node_of_body(body):
    return body.removeprefix("hello-") if body.startswith("hello-") else None


def service(p, svc_id):
    _, s = p.call("GET", f"/services/{svc_id}", expect=200)
    return s


def placements_nodes(p, svc_id, nodes_by_id):
    s = service(p, svc_id)
    return sorted(nodes_by_id[x["node_id"]] for x in s["placements"] if x["role"] != "stopped")


def sql(query):
    return dx("pp-m7-n1", "docker", "exec", "pg", "psql", "-tA", "-U", "polipan", "-d", "polipan", "-c", query)


class Load(threading.Thread):
    """Requests through the ingress of some nodes, 20 per second each. It counts the requests that do not answer 200."""

    def __init__(self, targets, host):
        super().__init__(daemon=True)
        self.targets, self.host, self.total, self.failed, self.stop_flag, self.errors = targets, host, 0, 0, False, []

    def run(self):
        while not self.stop_flag:
            for ip in self.targets:
                status, body = http_get(ip, self.host, timeout=5)
                self.total += 1
                if status != 200:
                    self.failed += 1
                    self.errors.append((ip, status, time.strftime("%H:%M:%S")))
            time.sleep(0.05)

    def finish(self):
        self.stop_flag = True
        self.join(timeout=15)


# ---- the scenario ----

def scenario(n1, n2, n3, ip1, ip2, ip3):
    names = {"n1": n1, "n2": n2, "n3": n3}
    ips = {"n1": ip1, "n2": ip2, "n3": ip3}
    p = logged_in()
    _, nodes = p.call("GET", "/nodes", expect=200)
    by_name = {n["name"]: n for n in nodes}
    by_id = {n["id"]: n["name"] for n in nodes}
    if sorted(by_name) != ["n1", "n2", "n3"] or any(n["state"] != "ready" for n in nodes):
        fail(f"nodes: {[(n['name'], n['state']) for n in nodes]}")
    subnets = {n["name"]: n["mesh_subnet"] for n in nodes}
    if len(set(subnets.values())) != 3:
        fail(f"mesh subnets are not unique: {subnets}")
    ok(f"REQ-AGENT-008: three nodes, one mesh subnet each: {subnets}")

    # ---- the mesh is real ----
    def mesh_up():
        for name in names:
            out = dx(names[name], "docker", "exec", "agent", "wg", "show", "wg0", "latest-handshakes", check=False)
            rows = [l for l in out.split("\n") if l.strip()]
            if len(rows) != 2 or any(l.split()[-1] == "0" for l in rows):
                return None
        return True
    wait("every node has a WireGuard handshake with both other nodes", mesh_up, timeout=120)
    for src in names:
        for dst in names:
            if src != dst:
                dx(names[src], "ping", "-c", "1", "-W", "3", by_name[dst]["mesh_ip"])
    ok("REQ-AGENT-008: the WireGuard mesh is complete: a handshake with each peer and a ping to each mesh address")

    # ---- services ----
    _, proj = p.call("POST", "/projects", {"name": "Shop"}, expect=201)
    _, envs = p.call("GET", f"/projects/{proj['id']}/environments", expect=200)
    prod = next(e for e in envs if e["kind"] == "production")
    staging = next((e for e in envs if e["kind"] == "staging"), None)
    if staging is None:
        _, staging = p.call("POST", f"/projects/{proj['id']}/environments", {"name": "staging", "kind": "staging"}, expect=201)
    alpine = "alpine:3.20"
    nginx = "nginx:alpine"
    web = create_service(p, prod["id"], "web", nginx, {"port": 8080, "command": httpd()}, failover="auto", domain="web.test")
    d = deploy_wait(p, web["id"])
    if placements_nodes(p, web["id"], by_id) != ["n1"]:
        fail(f"the scheduler must pick n1 first (tie by name): {placements_nodes(p, web['id'], by_id)}")
    for name in ("n1", "n2", "n3"):
        status, body = wait(f"web answers through the ingress of {name}", lambda ip=ips[name]: (lambda r: r if r[0] == 200 else None)(http_get(ip, "web.test")), timeout=60)
        if body != "hello-n1":
            fail(f"ingress of {name} answered {body!r}")
    ok("REQ-NODE-010: web runs on n1, and the ingress of n1, n2 and n3 all route to it (n2 and n3 over the mesh)")

    # ---- AC-NODE-03: move without failed requests ----
    load = Load([ips["n2"], ips["n3"]], "web.test")
    load.start()
    time.sleep(2)
    _, dep = p.call("POST", f"/services/{web['id']}/move", {"target_node_id": by_name["n2"]["id"]}, expect=202)
    dep_wait(p, dep["id"], timeout=240)
    time.sleep(2)
    load.finish()
    if load.failed or load.total < 50:
        fail(f"AC-NODE-03: {load.failed} of {load.total} requests failed during the move: {load.errors[:5]}")
    if placements_nodes(p, web["id"], by_id) != ["n2"] or containers(n2, web["id"]) == [] or containers(n1, web["id"], True):
        fail("AC-NODE-03: the container must run on n2 and be gone from n1")
    for name in ("n1", "n2", "n3"):
        _, body = http_get(ips[name], "web.test")
        if body != "hello-n2":
            fail(f"after the move the ingress of {name} answers {body!r}")
    ok(f"AC-NODE-03: web moved from n1 to n2 with blue-green, {load.total} requests through two ingress nodes, 0 failed")

    # ---- AC-NODE-09: a client on n3 calls web by its internal name over the mesh ----
    client = create_service(p, prod["id"], "client", alpine, {"port": 8080, "command": ["sh", "-c", "exec sleep 100000"], "health": {"type": "command", "command": ["true"]},
                            "placement": {"nodes": ["n3"]}})
    deploy_wait(p, client["id"])
    cname = containers(n3, client["id"])[0]
    name = alias_of(web["id"])
    for host in (name, name + ".polipan.internal"):
        out = dx(n3, "docker", "exec", cname, "wget", "-q", "-O-", "-T", "5", f"http://{host}:8080/", check=False)
        if out != "hello-n2":
            fail(f"AC-NODE-09: wget http://{host}:8080/ from n3 gave {out!r}")
    ok("AC-NODE-09: a client on n3 reaches web on n2 by its internal DNS name over the WireGuard mesh")

    # ---- REQ-SEC-106: environment isolation ----
    other_n3 = create_service(p, staging["id"], "other3", nginx, {"port": 8080, "command": httpd(), "placement": {"nodes": ["n3"]}})
    other_n2 = create_service(p, staging["id"], "other2", nginx, {"port": 8080, "command": httpd(), "placement": {"nodes": ["n2"]}})
    deploy_wait(p, other_n3["id"])
    deploy_wait(p, other_n2["id"])
    ip_o3 = container_ip(n3, containers(n3, other_n3["id"])[0])
    ip_o2 = container_ip(n2, containers(n2, other_n2["id"])[0])
    ip_web = container_ip(n2, containers(n2, web["id"])[0])

    def reach(src_node, src_ctr, ip):
        r = subprocess.run(["docker", "exec", src_node, "docker", "exec", src_ctr, "wget", "-q", "-O-", "-T", "4", f"http://{ip}:8080/"],
                           capture_output=True, text=True, timeout=60)
        return r.stdout.strip() if r.returncode == 0 else None
    if reach(n3, cname, ip_web) != "hello-n2":
        fail("REQ-SEC-106: a container must reach the containers of its own environment on another node")
    if reach(n3, cname, ip_o3) is not None:
        fail("REQ-SEC-106: a container of environment production reached a container of staging on the same node")
    if reach(n3, cname, ip_o2) is not None:
        fail("REQ-SEC-106: a container of environment production reached a container of staging on another node")
    o3ctr = containers(n3, other_n3["id"])[0]
    if reach(n3, o3ctr, ip_web) is not None:
        fail("REQ-SEC-106: a container of staging reached a container of production on another node")
    if "polipan_iso" not in dx(n3, "docker", "exec", "agent", "nft", "list", "tables"):
        fail("the agent did not write the isolation rules")
    ok("REQ-SEC-106: a container reaches its own environment (here, on another node), and cannot reach a container of another "
       "environment, neither on the same node nor on another node")

    # ---- AC-NODE-08: drain. A pinned service stays and is reported, then everything moves ----
    _, dr = p.call("POST", f"/nodes/{by_name['n3']['id']}/drain", expect=202)

    def drain_note():
        n = node_by_name(p, "n3")
        return n if n["state"] == "draining" and "client" in (n.get("drain_note") or "") else None
    n = wait("the drain reports what it could not move", drain_note, timeout=120)
    ok(f"REQ-NODE-003: the drain of n3 stays draining and says what is left: {n['drain_note'][:120]}")
    for pinned in (client, other_n3):
        _, cur = p.call("GET", f"/services/{pinned['id']}", expect=200)
        cfg = cur["config"]
        cfg.pop("placement", None)
        p.call("PATCH", f"/services/{pinned['id']}", {"config": cfg}, expect=200)
    wait("n3 ends in maintenance", lambda: (lambda n: n if n["state"] == "maintenance" else None)(node_by_name(p, "n3")), timeout=240, every=3)
    if containers(n3, client["id"], True) or containers(n3, other_n3["id"], True):
        fail("AC-NODE-08: containers are left on the drained node")
    if len(placements_nodes(p, client["id"], by_id)) != 1 or "n3" in placements_nodes(p, client["id"], by_id):
        fail("AC-NODE-08: client did not move")
    ok("AC-NODE-08: all placements moved away from n3 and the node ended in maintenance")
    p.call("POST", f"/nodes/{by_name['n3']['id']}/maintenance", {"enabled": False}, expect=200)
    wait("n3 is ready again", lambda: (lambda n: n if n["state"] == "ready" else None)(node_by_name(p, "n3")), timeout=30)

    # ---- AC-NODE-04: failover of a stateless service ----
    where = placements_nodes(p, web["id"], by_id)
    if where != ["n2"]:
        fail(f"web should still be on n2: {where}")
    t0 = time.time()
    run("docker", "pause", n2)
    try:
        def moved():
            status, body = http_get(ips["n1"], "web.test", timeout=2)
            return body if status == 200 and body in ("hello-n1", "hello-n3") else None
        body = wait("web answers from another node", moved, timeout=FAILOVER_BUDGET + 30, every=1)
        took = time.time() - t0
        if took > FAILOVER_BUDGET:
            fail(f"AC-NODE-04: the service needed {took:.0f} s, the target is {FAILOVER_BUDGET} s (node_down_after {NODE_DOWN_AFTER} s + 60 s)")
        ok(f"AC-NODE-04: node n2 stopped, web answers from {body.removeprefix('hello-')} after {took:.0f} s (target {FAILOVER_BUDGET} s)")
        _, ev = p.call("GET", "/audit-events?action=service.failover_start&limit=20", expect=200)
        items = [e for e in ev["items"] if e["action"] == "service.failover_start" and e["target_id"] == web["id"]]
        if not items or items[0]["actor_type"] != "system":
            fail(f"AC-NODE-04: no system audit event service.failover_start: {ev}")
        alerts = int(sql("SELECT count(*) FROM events WHERE topic = 'alert' AND payload->>'rule' IN ('node_down','service_failover')"))
        if alerts < 2:
            fail(f"AC-NODE-04: alerts: {alerts}")
        ok("AC-NODE-04: the failover has an audit event (actor system) and alerts (node_down, service_failover)")
        # While n2 is down: the split-brain guard has the new placement as the only one.
        if placements_nodes(p, web["id"], by_id) == ["n2"]:
            fail("the failed-over service must not be placed on n2 any more")
    finally:
        run("docker", "unpause", n2)

    # ---- AC-NODE-07: the node returns, the old container is removed and not started ----
    wait("n2 is ready again", lambda: (lambda n: n if n["state"] == "ready" else None)(node_by_name(p, "n2")), timeout=120)

    def old_gone():
        return not containers(n2, web["id"], True)
    wait("the old container of web is removed from n2", old_gone, timeout=90)
    running = sum(len(containers(n, web["id"])) for n in (n1, n2, n3))
    if running != 1:
        fail(f"AC-NODE-07: web runs {running} times")
    ok("AC-NODE-07: node n2 came back, its old web container was removed and not started, web runs once")

    # ---- T-E2E-08: failover with a volume ----
    data = create_service(p, prod["id"], "data", nginx, {"port": 8080, "placement": {"nodes": ["n2"]},
                          "command": ["sh", "-c", "mkdir -p /data && { [ -f /data/index.html ] || echo seed > /data/index.html; } && "
                                      "printf 'server { listen 8080; root /data; }' > /etc/nginx/conf.d/default.conf && exec nginx -g 'daemon off;'"]},
                          failover="auto", domain="data.test", volume="/data")
    deploy_wait(p, data["id"])
    _, cur = p.call("GET", f"/services/{data['id']}", expect=200)
    cfg = cur["config"]
    cfg.pop("placement", None)
    p.call("PATCH", f"/services/{data['id']}", {"config": cfg}, expect=200)
    dctr = containers(n2, data["id"])[0]
    dx(n2, "docker", "exec", dctr, "sh", "-c", "echo v1-data > /data/index.html")
    _, target = p.call("POST", "/backup-targets", {"name": "shared", "kind": "local", "config": {"path": "/var/lib/polipan/backups/shared"}}, expect=201)
    _, res = p.call("POST", f"/backup-targets/{target['id']}/test", expect=200)
    if not res["ok"]:
        fail(f"backup target: {res}")
    _, pol = p.call("POST", f"/services/{data['id']}/backup-policies",
                    {"volume_id": data["volume"]["id"], "target_ids": [target["id"]], "retention": {"keep_daily": 7}}, expect=201)
    p.call("POST", f"/backup-policies/{pol['id']}/run", expect=202)

    def backed_up():
        _, runs = p.call("GET", f"/backup-policies/{pol['id']}/runs", expect=200)
        for r in runs["items"]:
            if r["state"] == "failed":
                fail(f"backup run failed: {r.get('error')}")
            if r["state"] == "succeeded":
                return r
        return None
    wait("the volume backup succeeds", backed_up, timeout=180, every=3)
    dx(n2, "docker", "exec", dctr, "sh", "-c", "echo v2-after-backup > /data/index.html")
    s = service(p, data["id"])
    if not s.get("last_backup_at"):
        fail(f"the service must show the time of its last backup: {s.get('last_backup_at')}")
    ok("the volume of data is backed up (v1-data), then changed (v2-after-backup). The service shows the time of the last backup")

    # AC-NODE-05: no opt-in, the node goes down, the service is not moved and an alert says why.
    if s["allow_data_loss_failover"]:
        fail("allow_data_loss_failover must be off")
    run("docker", "pause", n2)
    try:
        def blocked():
            sv = service(p, data["id"])
            fs = sv.get("failover_status", {})
            return fs if fs.get("state") == "blocked" else None
        fs = wait("the failover of data is blocked", blocked, timeout=NODE_DOWN_AFTER + 60, every=2)
        if "allow_data_loss_failover" not in fs["reason"]:
            fail(f"AC-NODE-05: the reason is {fs['reason']!r}")
        time.sleep(15)
        if placements_nodes(p, data["id"], by_id) != ["n2"] or containers(n1, data["id"], True) or containers(n3, data["id"], True):
            fail("AC-NODE-05: the service must stay where it is")
        alerts = int(sql(f"SELECT count(*) FROM events WHERE topic = 'alert' AND payload->>'rule' = 'failover_blocked' AND object_id = '{data['id']}'"))
        if alerts != 1:
            fail(f"AC-NODE-05: {alerts} alerts failover_blocked")
        ok(f"AC-NODE-05: node n2 is down, data is not moved, the alert says why: {fs['reason']}")
    finally:
        run("docker", "unpause", n2)
    wait("n2 is ready again", lambda: (lambda n: n if n["state"] == "ready" else None)(node_by_name(p, "n2")), timeout=120)
    wait("data is healthy again", lambda: service(p, data["id"]).get("failover_status", {}).get("state") == "healthy" or None, timeout=60)

    # AC-NODE-06: with the opt-in, the service starts on another node with the restored snapshot.
    p.call("PATCH", f"/services/{data['id']}", {"allow_data_loss_failover": True}, expect=200)
    t0 = time.time()
    run("docker", "pause", n2)
    try:
        def restored():
            status, body = http_get(ips["n1"], "data.test", timeout=2)
            return body if status == 200 and body in ("v1-data", "v2-after-backup", "seed") else None
        body = wait("data answers from another node", restored, timeout=FAILOVER_BUDGET + 60, every=1)
        took = time.time() - t0
        if body != "v1-data":
            fail(f"AC-NODE-06: the restored data is {body!r}, want v1-data (the snapshot)")
        sv = service(p, data["id"])
        fs = sv.get("failover_status", {})
        if not fs.get("data_lost_since"):
            fail(f"AC-NODE-06: the loss of data is not recorded: {fs}")
        steps = sql("SELECT count(*) FROM deployment_steps s JOIN deployments d ON d.id = s.deployment_id WHERE d.trigger = 'failover' "
                    f"AND d.service_id = '{data['id']}' AND s.message LIKE '%is lost%'")
        if int(steps) < 1:
            fail("AC-NODE-06: the warning about lost data is not in the deployment")
        ok(f"AC-NODE-06: data restarted on another node with the snapshot ({body}) after {took:.0f} s. Data since {fs['data_lost_since']} is lost")
    finally:
        run("docker", "unpause", n2)
    wait("n2 is ready again", lambda: (lambda n: n if n["state"] == "ready" else None)(node_by_name(p, "n2")), timeout=120)
    wait("the old data container is removed from n2", lambda: not containers(n2, data["id"], True) or None, timeout=90)

    # ---- AC-NODE-10: the server cannot reach any node ----
    before = int(sql("SELECT count(*) FROM deployments WHERE trigger = 'failover'"))
    for n in (n1, n2, n3):
        run("docker", "exec", n, "docker", "stop", "agent")
    # The panel still answers: the server and the Caddy of n1 are separate containers.
    wait("all nodes look down", lambda: all(node_by_name(p, x)["state"] == "down" for x in ("n1", "n2", "n3")) or None, timeout=NODE_DOWN_AFTER + 60, every=2)
    time.sleep(40)  # the failover controller must have had many rounds
    after = int(sql("SELECT count(*) FROM deployments WHERE trigger = 'failover'"))
    states = sql("SELECT string_agg(state, ',') FROM nodes WHERE deleted_at IS NULL")
    reason = sql("SELECT string_agg(DISTINCT failover_reason, ' | ') FROM services WHERE failover_reason IS NOT NULL")
    for n in (n1, n2, n3):
        run("docker", "exec", n, "docker", "start", "agent")
    if after != before:
        fail(f"AC-NODE-10: {after - before} failover deployments started while no node was reachable")
    ok(f"AC-NODE-10: all nodes looked down ({states}), no failover started. The services say why: {reason[:100]}")
    for name in ("n1", "n2", "n3"):
        wait(f"{name} is ready again", lambda nm=name: (lambda n: n if n["state"] == "ready" else None)(node_by_name(p, nm)), timeout=120)
    print("M7 SCENARIO PASSED")


COMMANDS = {"setup": lambda: setup(sys.argv[2]), "wait-ready": lambda: wait_ready(sys.argv[2], sys.argv[3]),
            "scenario": lambda: scenario(*sys.argv[2:8])}

if __name__ == "__main__":
    COMMANDS[sys.argv[1]]()
