# syntax=docker/dockerfile:1.6

# Build stage — compiles the maping-server binary from the Go workspace.
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git ca-certificates

# Module fetches inside the build network can hit HTTP/2 stream resets against
# proxy.golang.org (`INTERNAL_ERROR` / unexpected EOF). Force HTTP/1.1 for the
# go tool's downloads and pin the proxy chain to make fetches reliable.
ENV GOPROXY=https://proxy.golang.org,direct \
    GODEBUG=http2client=0

WORKDIR /build

# The workspace pins four modules; the server binary needs proto (via a replace
# directive) and pulls its sums from go.work.sum. Copy the whole workspace so
# the build matches `make build` exactly. Cache mounts persist the module cache
# and compile cache across rebuilds, so only changed packages recompile.
COPY go.work go.work.sum ./
COPY proto ./proto
COPY client ./client
COPY server ./server
COPY example ./example

# Retry the build a few times: the cache mount keeps already-fetched modules, so
# each attempt after a transient network reset resumes instead of restarting.
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    for i in 1 2 3 4 5; do \
        CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-w -s" \
            -o /out/maping-server ./server/cmd/maping-server && break; \
        echo "build attempt $i failed; retrying in 5s..."; sleep 5; \
    done; \
    test -x /out/maping-server

# Runtime stage — the binary is self-contained (templates are Go string
# literals, Postgres migrations are go:embed), so no external assets are needed.
FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app
COPY --from=builder /out/maping-server .

# Dashboard (HTTP/1) + ingest (h2c gRPC) share this port; see MAPING_LISTEN.
EXPOSE 8080

ENTRYPOINT ["./maping-server"]
