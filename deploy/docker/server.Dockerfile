# SPDX-License-Identifier: Apache-2.0
# polipan-server with the web app inside. Build from the repository root:
#   docker build -f deploy/docker/server.Dockerfile -t polipan-server .

FROM node:20-alpine@sha256:fb4cd12c85ee03686f6af5362a0b0d56d50c58a04632e6c0fb8363f609372293 AS web
WORKDIR /src
COPY web/package.json web/package-lock.json web/
RUN cd web && npm ci --no-audit --no-fund
COPY api api
COPY web web
# The build writes into server/internal/web/dist (see web/vite.config.ts).
RUN cd web && npm run build

FROM golang:1.23-bookworm@sha256:167053a2bb901972bf2c1611f8f52c44d5fe7e762e5cab213708d82c421614db AS build
ARG VERSION=dev
ARG COMMIT=unknown
ARG TARGETOS=linux
ARG TARGETARCH
# The release build gives the digest of the agent image, and the install script of the panel pins it (REQ-OSS-004).
ARG AGENT_IMAGE_DIGEST=
ARG AGENT_IMAGE_REPO=ghcr.io/polipan/polipan-agent
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/server/internal/web/dist server/internal/web/dist
RUN if [ -n "$AGENT_IMAGE_DIGEST" ]; then \
      sh deploy/release/stamp.sh server/internal/api/install-agent.sh AGENT_IMAGE_DIGEST="$AGENT_IMAGE_DIGEST" AGENT_IMAGE_REPO="$AGENT_IMAGE_REPO"; \
    fi
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -buildvcs=false \
    -ldflags "-s -w -X github.com/polipan/polipan/server/internal/version.Version=$VERSION -X github.com/polipan/polipan/server/internal/version.Commit=$COMMIT" \
    -o /out/polipan-server ./server/cmd/polipan-server

# Git builds (M2): the server clones with git (https and ssh), runs "railpack prepare" and talks to
# buildkitd with buildctl. Keep the railpack version equal to build.DefaultRailpackFrontend.
FROM moby/buildkit:v0.34.0@sha256:b059f8d7226d0b326bc871af489a5dddf65e36af4d20d14251b072974d9ee87c AS buildkit

FROM alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc AS railpack
ARG RAILPACK_VERSION=0.40.1
ARG TARGETARCH
# The download is checked against a known checksum. Change both values when RAILPACK_VERSION changes.
ARG RAILPACK_SHA256_AMD64=2842de93e68713af9037e0bc0a398d7da78f3b96aa4804303a638db2bc69bd30
ARG RAILPACK_SHA256_ARM64=c24a064b586b8f4f8c2fab44dd5ef19253e4c6cc4e1df793b3ae19cd87f7a5d4
RUN apk add --no-cache curl && \
    case "$TARGETARCH" in arm64) arch=arm64; sum=$RAILPACK_SHA256_ARM64 ;; *) arch=x86_64; sum=$RAILPACK_SHA256_AMD64 ;; esac && \
    curl -fsSL -o /tmp/railpack.tar.gz "https://github.com/railwayapp/railpack/releases/download/v${RAILPACK_VERSION}/railpack-v${RAILPACK_VERSION}-${arch}-unknown-linux-musl.tar.gz" && \
    echo "$sum  /tmp/railpack.tar.gz" | sha256sum -c - && \
    tar -xz -C /usr/local/bin -f /tmp/railpack.tar.gz railpack && rm /tmp/railpack.tar.gz

FROM alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc
RUN apk add --no-cache git openssh-client ca-certificates && \
    adduser -D -H -u 65532 nonroot && mkdir -p /var/lib/polipan && chown 65532:65532 /var/lib/polipan
COPY --from=buildkit /usr/bin/buildctl /usr/local/bin/buildctl
COPY --from=railpack /usr/local/bin/railpack /usr/local/bin/railpack
COPY --from=build /out/polipan-server /polipan-server
USER 65532:65532
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=5s --start-period=10s --retries=3 CMD ["/polipan-server", "healthcheck"]
ENTRYPOINT ["/polipan-server"]
