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

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/polipan-server /polipan-server
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=5s --start-period=10s --retries=3 CMD ["/polipan-server", "healthcheck"]
ENTRYPOINT ["/polipan-server"]
