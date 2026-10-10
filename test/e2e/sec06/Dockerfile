# SPDX-License-Identifier: Apache-2.0
# AC-SEC-06 fixture: build steps must reach the public internet but nothing internal.
# TARGETS is a list of host:port (PostgreSQL, VictoriaLogs, VictoriaMetrics, registry, a mesh
# address, the server, the bridge gateway). Every connection must fail; the build fails if one works.
FROM alpine:3.20
ARG TARGETS
# A public request: the package download must work.
RUN apk add --no-cache netcat-openbsd >/dev/null && echo "PUBLIC_OK"
RUN set -u; for t in $TARGETS; do \
      host=${t%:*}; port=${t##*:}; \
      if nc -z -w 3 "$host" "$port"; then echo "REACHED $t"; exit 1; fi; \
      echo "BLOCKED $t"; \
    done; echo "ALL_BLOCKED"
