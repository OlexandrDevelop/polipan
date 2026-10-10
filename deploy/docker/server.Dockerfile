# SPDX-License-Identifier: Apache-2.0
# polipan-server with the web app inside. Build from the repository root:
#   docker build -f deploy/docker/server.Dockerfile -t polipan-server .

FROM node:20-alpine AS web
WORKDIR /src
COPY web/package.json web/package-lock.json web/
RUN cd web && npm ci --no-audit --no-fund
COPY api api
COPY web web
# The build writes into server/internal/web/dist (see web/vite.config.ts).
RUN cd web && npm run build

FROM golang:1.23-bookworm AS build
ARG VERSION=dev
ARG COMMIT=unknown
ARG TARGETOS=linux
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/server/internal/web/dist server/internal/web/dist
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags "-s -w -X github.com/polipan/polipan/server/internal/version.Version=$VERSION -X github.com/polipan/polipan/server/internal/version.Commit=$COMMIT" \
    -o /out/polipan-server ./server/cmd/polipan-server

# Git builds (M2): the server clones with git (https and ssh), runs "railpack prepare" and talks to
# buildkitd with buildctl. Keep the railpack version equal to build.DefaultRailpackFrontend.
FROM moby/buildkit:v0.34.0 AS buildkit

FROM alpine:3.20 AS railpack
ARG RAILPACK_VERSION=0.40.1
ARG TARGETARCH
RUN apk add --no-cache curl && \
    case "$TARGETARCH" in arm64) arch=arm64 ;; *) arch=x86_64 ;; esac && \
    curl -fsSL "https://github.com/railwayapp/railpack/releases/download/v${RAILPACK_VERSION}/railpack-v${RAILPACK_VERSION}-${arch}-unknown-linux-musl.tar.gz" \
      | tar -xz -C /usr/local/bin railpack

FROM alpine:3.20
RUN apk add --no-cache git openssh-client ca-certificates && \
    adduser -D -H -u 65532 nonroot && mkdir -p /var/lib/polipan && chown 65532:65532 /var/lib/polipan
COPY --from=buildkit /usr/bin/buildctl /usr/local/bin/buildctl
COPY --from=railpack /usr/local/bin/railpack /usr/local/bin/railpack
COPY --from=build /out/polipan-server /polipan-server
USER 65532:65532
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=5s --start-period=10s --retries=3 CMD ["/polipan-server", "healthcheck"]
ENTRYPOINT ["/polipan-server"]
