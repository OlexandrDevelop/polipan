# SPDX-License-Identifier: Apache-2.0
# A small agent image for the M8 test (and the M4 test with the alert part): the binary is built outside.
FROM restic/restic:0.17.3 AS restic

FROM alpine:3.20
RUN apk add --no-cache ca-certificates openssh-client-default tzdata wireguard-tools iproute2 iptables iptables-legacy nftables
COPY --from=restic /usr/bin/restic /usr/local/bin/restic
COPY polipan-agent /polipan-agent
ENTRYPOINT ["/polipan-agent"]
