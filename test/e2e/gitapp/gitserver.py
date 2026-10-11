# SPDX-License-Identifier: Apache-2.0
"""Fake Git server for T-E2E-02 (no Internet needed for the source).

It serves one repository /app.git over smart HTTP (git http-backend) and has a helper:
  POST /commit?version=N[&branch=B]
                          -> writes a new app version (Dockerfile + index.html), commits it on
                             the branch (default main) and answers the commit id.
It also plays the part of a Gitea API for the preview test of M9 (T-E2E-06): it keeps the commit statuses and
the comments that Polipan posts, and shows them with GET /_test/state.
Run: python3 -I gitserver.py <port> <work dir>
"""
import http.server
import json
import os
import re
import subprocess
import sys

PORT = int(sys.argv[1])
ROOT = sys.argv[2]
BARE = os.path.join(ROOT, "app.git")
WORK = os.path.join(ROOT, "work")
ENV = dict(os.environ, GIT_AUTHOR_NAME="Ann", GIT_AUTHOR_EMAIL="ann@example.com",
           GIT_COMMITTER_NAME="Ann", GIT_COMMITTER_EMAIL="ann@example.com")


def git(*args, cwd=None):
    return subprocess.run(["git", *args], cwd=cwd, env=ENV, check=True, capture_output=True, text=True).stdout.strip()


def setup():
    os.makedirs(ROOT, exist_ok=True)
    if not os.path.isdir(BARE):
        git("init", "-q", "--bare", "-b", "main", BARE)
        git("config", "http.receivepack", "false", cwd=BARE)
        git("config", "uploadpack.allowReachableSHA1InWant", "true", cwd=BARE)
        git("clone", "-q", BARE, WORK)


def commit(version, branch="main"):
    if branch == "main":
        git("checkout", "-q", "main", cwd=WORK)
    else:
        known = subprocess.run(["git", "rev-parse", "--verify", "-q", "refs/heads/" + branch], cwd=WORK, env=ENV, capture_output=True).returncode == 0
        git("checkout", "-q", branch, cwd=WORK) if known else git("checkout", "-q", "-B", branch, "main", cwd=WORK)
    with open(os.path.join(WORK, "Dockerfile"), "w") as f:
        f.write("FROM nginx:alpine\nCOPY index.html /usr/share/nginx/html/index.html\n")
    with open(os.path.join(WORK, "index.html"), "w") as f:
        f.write(f"polipan git app version {version}\n")
    git("add", "-A", cwd=WORK)
    git("commit", "-q", "-m", f"version {version}", cwd=WORK)
    git("push", "-q", "origin", "HEAD:refs/heads/" + branch, cwd=WORK)
    sha = git("rev-parse", "HEAD", cwd=WORK)
    git("checkout", "-q", "main", cwd=WORK)
    return sha


# What Polipan posted through the fake Gitea API.
STATUSES = []
COMMENTS = []
API_STATUS = re.compile(r"^/api/v1/repos/(.+)/statuses/([0-9a-f]+)$")
API_COMMENTS = re.compile(r"^/api/v1/repos/(.+)/issues/(\d+)/comments$")
API_COMMENT = re.compile(r"^/api/v1/repos/(.+)/issues/comments/(\d+)$")


class Handler(http.server.BaseHTTPRequestHandler):
    def reply(self, status, data):
        raw = json.dumps(data).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def read_json(self):
        length = int(self.headers.get("Content-Length") or 0)
        return json.loads(self.rfile.read(length) or b"{}")

    def api(self, method):
        path = self.path.split("?", 1)[0]
        if method == "GET" and path == "/_test/state":
            return self.reply(200, {"statuses": STATUSES, "comments": COMMENTS})
        m = API_STATUS.match(path)
        if method == "POST" and m:
            body = self.read_json()
            STATUSES.append({"repo": m.group(1), "sha": m.group(2), **body})
            return self.reply(201, {})
        m = API_COMMENTS.match(path)
        if method == "POST" and m:
            body = self.read_json()
            c = {"id": len(COMMENTS) + 1, "repo": m.group(1), "number": int(m.group(2)), "body": body.get("body", ""), "writes": 1}
            COMMENTS.append(c)
            return self.reply(201, {"id": c["id"]})
        m = API_COMMENT.match(path)
        if method == "PATCH" and m:
            body = self.read_json()
            for c in COMMENTS:
                if c["id"] == int(m.group(2)):
                    c["body"], c["writes"] = body.get("body", ""), c["writes"] + 1
                    return self.reply(200, {"id": c["id"]})
            return self.reply(404, {})
        return False

    def do_POST(self):
        if self.path.startswith("/commit"):
            q = dict(p.split("=", 1) for p in self.path.split("?", 1)[-1].split("&") if "=" in p)
            sha = commit(q.get("version", "1"), q.get("branch", "main")).encode()
            self.send_response(200)
            self.send_header("Content-Length", str(len(sha)))
            self.end_headers()
            self.wfile.write(sha)
            return
        if self.path.startswith("/api/v1/") or self.path.startswith("/_test/"):
            if self.api("POST") is not False:
                return
        self.backend()

    def do_PATCH(self):
        if self.api("PATCH") is False:
            self.reply(404, {})

    def do_GET(self):
        if self.path.startswith("/_test/") and self.api("GET") is not False:
            return
        self.backend()

    def backend(self):
        path, _, query = self.path.partition("?")
        length = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(length) if length else b""
        env = dict(os.environ, GIT_PROJECT_ROOT=ROOT, GIT_HTTP_EXPORT_ALL="1", PATH_INFO=path, QUERY_STRING=query,
                   REQUEST_METHOD=self.command, CONTENT_TYPE=self.headers.get("Content-Type", ""),
                   CONTENT_LENGTH=str(len(body)), GIT_PROTOCOL=self.headers.get("Git-Protocol", ""),
                   HTTP_CONTENT_ENCODING=self.headers.get("Content-Encoding", ""))
        out = subprocess.run(["git", "http-backend"], input=body, env=env, capture_output=True).stdout
        head, _, data = out.partition(b"\r\n\r\n")
        status = 200
        headers = []
        for line in head.split(b"\r\n"):
            k, _, v = line.decode().partition(":")
            if k.lower() == "status":
                status = int(v.strip().split()[0])
            elif k:
                headers.append((k, v.strip()))
        self.send_response(status)
        for k, v in headers:
            self.send_header(k, v)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


if __name__ == "__main__":
    setup()
    http.server.ThreadingHTTPServer(("0.0.0.0", PORT), Handler).serve_forever()
