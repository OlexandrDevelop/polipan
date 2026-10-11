# SPDX-License-Identifier: Apache-2.0
"""Traffic generator. It runs INSIDE the VM (verify.py copies it there) and sends requests through the Caddy of the node.

  python3 load.py HOST OUT_FILE [RATE]

Every request has its own connection. One line per request is written to OUT_FILE: "<time> <status> <body>".
It runs until the file OUT_FILE.stop exists.
"""
import http.client
import os
import sys
import threading
import time

host, out = sys.argv[1], sys.argv[2]
rate = float(sys.argv[3]) if len(sys.argv) > 3 else 20.0
lock = threading.Lock()
f = open(out, "w", buffering=1)


def one():
    t0 = time.time()
    try:
        conn = http.client.HTTPConnection("127.0.0.1", 80, timeout=10)
        conn.request("GET", "/", headers={"Host": host, "Connection": "close"})
        r = conn.getresponse()
        rec = f"{t0:.3f} {r.status} {r.read().decode('latin1').strip()[:60]}"
        conn.close()
    except Exception as e:  # a failed request is a result, not a crash
        rec = f"{t0:.3f} 0 {e!r}"
    with lock:
        f.write(rec.replace("\n", " ") + "\n")


nxt = time.time()
while not os.path.exists(out + ".stop"):
    threading.Thread(target=one, daemon=True).start()
    nxt += 1.0 / rate
    time.sleep(max(0, nxt - time.time()))
time.sleep(2)  # let the last requests end
