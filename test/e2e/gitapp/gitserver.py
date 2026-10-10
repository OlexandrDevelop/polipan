# SPDX-License-Identifier: Apache-2.0
"""Fake Git server for T-E2E-02 (no Internet needed for the source).

It serves one repository /app.git over smart HTTP (git http-backend) and has a helper:
  POST /commit?version=N  -> writes a new app version (Dockerfile + index.html), commits it on
                             main and answers the commit id.
Run: python3 -I gitserver.py <port> <work dir>
"""
import http.server
import os
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


def commit(version):
    with open(os.path.join(WORK, "Dockerfile"), "w") as f:
        f.write("FROM nginx:alpine\nCOPY index.html /usr/share/nginx/html/index.html\n")
    with open(os.path.join(WORK, "index.html"), "w") as f:
        f.write(f"polipan git app version {version}\n")
    git("add", "-A", cwd=WORK)
    git("commit", "-q", "-m", f"version {version}", cwd=WORK)
    git("push", "-q", "origin", "HEAD:main", cwd=WORK)
    return git("rev-parse", "HEAD", cwd=WORK)


class Handler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        if self.path.startswith("/commit"):
            version = self.path.split("version=", 1)[-1]
            sha = commit(version).encode()
            self.send_response(200)
            self.send_header("Content-Length", str(len(sha)))
            self.end_headers()
            self.wfile.write(sha)
            return
        self.backend()

    def do_GET(self):
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
