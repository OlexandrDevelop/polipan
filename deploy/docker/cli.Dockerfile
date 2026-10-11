# SPDX-License-Identifier: Apache-2.0
# polipan command line client. Build from the repository root:
#   docker build -f deploy/docker/cli.Dockerfile -t polipan-cli .

FROM golang:1.23-bookworm@sha256:167053a2bb901972bf2c1611f8f52c44d5fe7e762e5cab213708d82c421614db AS build
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
