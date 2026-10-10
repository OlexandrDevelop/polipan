# SPDX-License-Identifier: Apache-2.0
# polipan command line client. Build from the repository root:
#   docker build -f deploy/docker/cli.Dockerfile -t polipan-cli .

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
    -ldflags "-s -w -X github.com/polipan/polipan/cli/internal/version.Version=$VERSION -X github.com/polipan/polipan/cli/internal/version.Commit=$COMMIT" \
    -o /out/polipan ./cli/cmd/polipan

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/polipan /polipan
ENTRYPOINT ["/polipan"]
