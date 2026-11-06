# plgv container image.
#   docker build -t plgv --build-arg VERSION=$(git describe --tags --always) .
#   docker run --rm plgv run -addr valkey:6379 -duration 30s
#   docker run --rm -p 127.0.0.1:8080:8080 -v plgv-data:/data plgv web -addr 0.0.0.0:8080 -allow-remote -allowed-host localhost,127.0.0.1
# See docs/deployment.md.

# --- Stage 1: build a static binary ---
FROM golang:1.25-alpine AS builder
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/plgv ./cmd/plgv

# --- Stage 2: a small runtime that does not run as root ---
FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata \
 && addgroup -S -g 10001 plgv && adduser -S -u 10001 -G plgv -h /data plgv \
 && mkdir -p /data /config && chown plgv:plgv /data
COPY --from=builder /out/plgv /usr/local/bin/plgv
COPY LICENSE THIRD_PARTY.md /usr/share/doc/plgv/
USER 10001:10001
WORKDIR /data
# Stored runs and saved settings of `plgv web` live here.
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["plgv"]
CMD ["help"]
