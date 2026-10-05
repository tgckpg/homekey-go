# syntax=docker/dockerfile:1
ARG GO_VERSION=1.26
FROM golang:${GO_VERSION}-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -mod=readonly -buildvcs=false -trimpath \
    -ldflags='-s -w' -o /out/homekey ./cmd/homekey

FROM debian:bookworm-slim
ARG BUILD_VERSION=0.0.3
ARG BUILD_ARCH=amd64
LABEL io.hass.type="app" \
      io.hass.version="${BUILD_VERSION}" \
      io.hass.arch="${BUILD_ARCH}" \
      org.opencontainers.image.title="homekey-go" \
      org.opencontainers.image.licenses="Apache-2.0"
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates jq tzdata && rm -rf /var/lib/apt/lists/*
COPY --from=builder /out/homekey /usr/local/bin/homekey
COPY ha-app/run.sh /usr/local/bin/homekey-entrypoint
COPY LICENSE NOTICE /usr/share/doc/homekey-go/
RUN chmod 0755 /usr/local/bin/homekey-entrypoint
WORKDIR /data
ENTRYPOINT ["/usr/local/bin/homekey-entrypoint"]
