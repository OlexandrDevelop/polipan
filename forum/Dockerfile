# SPDX-License-Identifier: Apache-2.0
FROM golang:1.26-bookworm@sha256:d9c68c2c51161e12fd77e4c6320687c9cd86e1af1e3ad6e6cd63ff970641453c AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /forum .

FROM alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc
RUN adduser -D -u 10001 forum
COPY --from=build /forum /usr/local/bin/forum
USER 10001
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=4s --start-period=10s CMD ["forum", "healthcheck"]
ENTRYPOINT ["forum"]
CMD ["serve"]
