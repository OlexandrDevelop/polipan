# SPDX-License-Identifier: Apache-2.0
"""Seeds VictoriaMetrics with 60 days of one metric and VictoriaLogs with lines of two environments (T-E2E-15).
Usage: seed.py ORG PROJECT PROD_ENV PROD_SERVICE STG_ENV STG_SERVICE NODE
It runs in a container on the control-plane network. The stores have basic auth polipan:vpw."""
import base64
import json
import sys
import time
import urllib.request

org, project, prod_env, prod_svc, stg_env, stg_svc, node = sys.argv[1:8]
AUTH = "Basic " + base64.b64encode(b"polipan:vpw").decode()


def post(url, body, ctype):
    req = urllib.request.Request(url, body.encode(), method="POST", headers={"Authorization": AUTH, "Content-Type": ctype})
    urllib.request.urlopen(req, timeout=120).read()


now = int(time.time())
# 60 days of samples every 5 minutes. The value is the day number, so a chart is not flat.
lines = []
for i in range(60 * 288 + 1):
    ts = (now - i * 300) * 1000
    lines.append(f'polipan_container_cpu_usage_millis{{org="{org}",project="{project}",environment="{prod_env}",service="{prod_svc}",'
                 f'release="rel_seed",node="{node}",replica="0"}} {100 + (i // 288)} {ts}')
post("http://vm:8428/api/v1/import/prometheus", "\n".join(lines) + "\n", "text/plain")


def log(ts, env, svc, msg, level="info"):
    return json.dumps({"_time": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(ts)), "_msg": msg, "org": org, "project": project,
                       "environment": env, "service": svc, "node": node, "level": level, "stream": "stdout", "release": "rel_seed",
                       "container": "seed"})


rows = []
for k in range(5):
    rows.append(log(now - 3600 + k, prod_env, prod_svc, f"timeout calling upstream {k}", "error"))
    rows.append(log(now - 3500 + k, prod_env, prod_svc, f"request {k} ok"))
    rows.append(log(now - 3400 + k, stg_env, stg_svc, f"timeout in staging {k}", "error"))
rows.append(log(now - 45 * 86400, prod_env, prod_svc, "timeout in the old days", "error"))
post("http://vl:9428/insert/jsonline?_stream_fields=org,project,environment,service,node&_msg_field=_msg&_time_field=_time",
     "\n".join(rows) + "\n", "application/stream+json")
print("seeded", len(lines), "samples and", len(rows), "log lines")
