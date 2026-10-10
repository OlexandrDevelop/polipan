# SPDX-License-Identifier: Apache-2.0
# A small agent image for the M8 test: the binary is built outside.
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata wireguard-tools iproute2 iptables iptables-legacy nftables
COPY polipan-agent /polipan-agent
ENTRYPOINT ["/polipan-agent"]
