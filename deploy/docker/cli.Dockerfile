# SPDX-License-Identifier: Apache-2.0
# polipan command line client. Build from the repository root:
#   docker build -f deploy/docker/cli.Dockerfile -t polipan-cli .

FROM golang:1.26-bookworm@sha256:d9c68c2c51161e12fd77e4c6320687c9cd86e1af1e3ad6e6cd63ff970641453c AS build
ARG VERSION=dev
ARG COMMIT=unknown
ARG TARGETOS=linux
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -buildvcs=false \
    -ldflags "-s -w -X github.com/polipan/polipan/cli/internal/version.Version=$VERSION -X github.com/polipan/polipan/cli/internal/version.Commit=$COMMIT" \
    -o /out/polipan ./cli/cmd/polipan

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/polipan /polipan
ENTRYPOINT ["/polipan"]
