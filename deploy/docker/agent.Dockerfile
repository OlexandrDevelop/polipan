# SPDX-License-Identifier: Apache-2.0
# polipan-agent. Build from the repository root:
#   docker build -f deploy/docker/agent.Dockerfile -t polipan-agent .
# The agent normally runs as a systemd service on the node. This image is for tests and for
# the agent of the control-plane host.

FROM golang:1.23-bookworm AS build
ARG VERSION=dev
ARG COMMIT=unknown
ARG TARGETOS=linux
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags "-s -w -X github.com/polipan/polipan/agent/internal/version.Version=$VERSION -X github.com/polipan/polipan/agent/internal/version.Commit=$COMMIT" \
    -o /out/polipan-agent ./agent/cmd/polipan-agent

# restic (backups) and an ssh client (SFTP targets) are part of the image. On a node without the
# image, install both packages: see docs/operations/backups.md.
FROM restic/restic:0.17.3 AS restic

FROM alpine:3.20
# WireGuard tools, iproute2, iptables (Docker forwarding rules) and nftables (environment firewall) are for the mesh (M7).
RUN apk add --no-cache ca-certificates openssh-client-default tzdata wireguard-tools iproute2 iptables iptables-legacy nftables
COPY --from=restic /usr/bin/restic /usr/local/bin/restic
COPY --from=build /out/polipan-agent /polipan-agent
ENTRYPOINT ["/polipan-agent"]
