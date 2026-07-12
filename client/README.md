# mAPI-ng Client

`github.com/arhuman/maping/client` — hosted, zero-config API observability for Go services.

The client collects per-endpoint request metrics (rate, latency, error rate, bandwidth) using
client-side DDSketch aggregation, and ships compact Summaries to the mAPI-ng collector over gRPC.
**License: MIT** (see `LICENSE`).

---

## 60-second quickstart

**1. Install**

```bash
go get github.com/arhuman/maping/client
```

The Gin adapter lives in its own module (`github.com/arhuman/maping/client/gin`) so the core
client stays free of web-framework dependencies. If you use Gin, install it as well:

```bash
go get github.com/arhuman/maping/client/gin
```

**2. Wire the middleware**

Register the mAPI-ng middleware **above** `gin.Recovery()` so panics are recorded as 5xx
before Recovery converts them, without altering host behavior.

```go
import (
    maping    "github.com/arhuman/maping/client"
    mapinggin "github.com/arhuman/maping/client/gin"
)

func main() {
    // One recorder for the process lifetime.
    rec := maping.NewRecorder(maping.WithService("my-api"))

    r := gin.New()
    r.Use(mapinggin.MiddlewareWithRecorder(rec)) // above Recovery
    r.Use(gin.Recovery())

    // ... register routes ...

    srv := &http.Server{Addr: ":9090", Handler: r}
    // start srv in a goroutine ...

    // On shutdown: stop HTTP first, then flush the recorder.
    srv.Shutdown(ctx)
    rec.Shutdown(ctx)
}
```

See [`example/main.go`](../example/main.go) for the complete runnable version including
signal handling and correct shutdown ordering.

**3. Activate**

```bash
export MAPING_KEY=your-ingest-key
```

That is the only required configuration. Everything else is inferred.

---

## No-op / zero-config guarantee

When `MAPING_KEY` is absent (or the resolved key is empty), `NewRecorder` returns a no-op
recorder. Every method is safe and does nothing; no goroutine starts. The middleware is always
safe to add to a codebase — activation is decoupled from the code change.

The client also fails open: transport errors, bad config values, and internal panics are logged
once at `Warn` via the host's logger and never block or crash the host process.

---

## Core API

```go
// NewRecorder resolves config and returns a Recorder. Returns a no-op recorder
// when no ingest key is resolved.
func NewRecorder(opts ...Option) *Recorder

// Observe records one completed request. Safe on a no-op recorder.
// Safe for concurrent use.
func (*Recorder) Observe(rec Record)

// Shutdown flushes the last window and drains pending uploads.
// Call AFTER http.Server.Shutdown so no request is still writing a Record.
// Idempotent. Bounded by ctx.
func (*Recorder) Shutdown(ctx context.Context) error
```

`SdkVersion = "0.1.0"`.

---

## Gin adapter

Module: `github.com/arhuman/maping/client/gin`

```go
// Middleware creates its own Recorder from opts. Use when you do not need
// to call Shutdown (fire-and-forget).
func Middleware(opts ...maping.Option) gin.HandlerFunc

// MiddlewareWithRecorder binds to a caller-owned Recorder so you control
// its lifecycle and can call rec.Shutdown on process exit.
func MiddlewareWithRecorder(rec *maping.Recorder) gin.HandlerFunc
```

Unmatched routes (where `c.FullPath()` returns `""`) are silently skipped to avoid
emitting raw paths and exploding series cardinality.

**Registration order is required:** the mAPI-ng middleware must be registered before
`gin.Recovery()`. See the quickstart above.

---

## TLS and endpoint

The default endpoint is `https://ingest.mapi-ng.dev` (the hosted collector). An `http://`
endpoint is permitted for local or dev use and switches the transport to H2C
(cleartext HTTP/2), which allows gRPC without TLS. Any other scheme is invalid config
(falls back to no-op recorder with a one-time `Warn` log).

---

## Configuration

Precedence for every setting: **code option > env var > default**.

| Setting | Code option | Env var | Default |
|---|---|---|---|
| Ingest key | `WithKey` | `MAPING_KEY` | *(none — absent means no-op)* |
| Endpoint | `WithEndpoint` | `MAPING_ENDPOINT` | `https://ingest.mapi-ng.dev` |
| Service name | `WithService` | `MAPING_SERVICE` | binary name (via `OTEL_SERVICE_NAME` or `os.Args[0]`) |
| Instance id | `WithInstance` | `MAPING_INSTANCE` | `$HOSTNAME` env, else OS hostname (`os.Hostname()`) |
| Flush window | `WithFlushWindow` | `MAPING_FLUSH_SECONDS` | 10s |

For full validation rules, precedence details, and invalid-config behavior see
[`CONFIG.md`](CONFIG.md).
