# SPDX-License-Identifier: Apache-2.0
# A small server image for the M7 test: the binary is built outside (no web build, no Git tools).
FROM alpine:3.20
RUN adduser -D -H -u 65532 nonroot && mkdir -p /var/lib/polipan && chown 65532:65532 /var/lib/polipan
COPY polipan-server /polipan-server
USER 65532:65532
ENTRYPOINT ["/polipan-server"]
