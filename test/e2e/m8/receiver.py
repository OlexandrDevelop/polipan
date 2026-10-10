# SPDX-License-Identifier: Apache-2.0
"""A fake alert receiver for the M8 test. It acts as the Telegram bot API, a Slack hook and a webhook.
POST /bot<token>/sendMessage, POST /slack, POST /hook keep the body. GET /messages gives the list as JSON.
POST /fail?on=1 makes every answer a 500 (off with on=0)."""
import json
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

LOCK = threading.Lock()
MESSAGES = []
STATE = {"fail": False}


class H(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *a):
        pass

    def reply(self, code, body):
        b = body.encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(b)))
        self.end_headers()
        self.wfile.write(b)

    def do_GET(self):
        if self.path.startswith("/messages"):
            with LOCK:
                self.reply(200, json.dumps(MESSAGES))
        else:
            self.reply(404, "{}")

    def do_POST(self):
        n = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(n).decode()
        path = self.path.split("?")[0]
        if path == "/fail":
            STATE["fail"] = "on=1" in self.path
            return self.reply(200, "{}")
        if STATE["fail"]:
            return self.reply(500, '{"ok":false}')
        kind = "telegram" if path.startswith("/bot") else "slack" if path == "/slack" else "webhook"
        with LOCK:
            MESSAGES.append({"kind": kind, "path": path, "body": body, "signature": self.headers.get("X-Polipan-Signature", "")})
        self.reply(200, '{"ok":true}')


ThreadingHTTPServer(("0.0.0.0", int(sys.argv[1])), H).serve_forever()
