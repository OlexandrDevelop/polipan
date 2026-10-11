# SPDX-License-Identifier: Apache-2.0
# A small server image for the M8 test (and the M4 test with the alert part): the binary is built outside (no web build,
# no Git tools). It has the health check that the compose file of the installer waits for.
FROM alpine:3.20
RUN adduser -D -H -u 65532 nonroot && mkdir -p /var/lib/polipan && chown 65532:65532 /var/lib/polipan
COPY polipan-server /polipan-server
USER 65532:65532
HEALTHCHECK --interval=5s --timeout=5s --retries=20 CMD ["/polipan-server", "healthcheck"]
ENTRYPOINT ["/polipan-server"]
