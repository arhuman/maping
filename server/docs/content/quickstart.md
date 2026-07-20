# Quickstart

## Run the stack

```bash
make local
```

This creates `.env` from `env.sample`, builds the server image, starts
ClickHouse and Postgres, and applies the schema at boot. Open the dashboard
at `http://localhost:8080`. Auth is off by default in this mode: Postgres
stays unused until `MAPING_POSTGRES_DSN` is set, so the dashboard is
immediately reachable at `/`.

To see the dashboard populated without writing any code:

```bash
make generate-traffic            # tune volume with ROUNDS, e.g. make generate-traffic ROUNDS=30
```

This runs the bundled example service against the local stack, fires
requests across every route (2xx, labelled 5xx, an aborted request, and a
downstream call), waits for a flush, and stops. Reload the dashboard to see
the `example-api` service with its endpoints, error classes, and per-instance
gauges.

## Instrument a Go service

Install the core client (no web-framework dependencies):

```bash
go get github.com/arhuman/maping/client
```

If the service uses Gin, install the adapter too. It lives in its own module
so importing the core client never pulls Gin into a binary that does not use
it:

```bash
go get github.com/arhuman/maping/client/gin
```

Wire the middleware above `gin.Recovery()`, so a panic is recorded as a 5xx
before Recovery converts it, without changing how the panic is handled:

```go
import (
	maping    "github.com/arhuman/maping/client"
	mapinggin "github.com/arhuman/maping/client/gin"
)

func main() {
	rec := maping.NewRecorder(maping.WithService("my-api")) // one recorder for the process lifetime

	r := gin.New()
	r.Use(mapinggin.MiddlewareWithRecorder(rec)) // above Recovery
	r.Use(gin.Recovery())

	// ... register routes ...

	srv := &http.Server{Addr: ":9090", Handler: r}
	// start srv in a goroutine ...

	// On shutdown: stop the HTTP server first, then flush the recorder.
	srv.Shutdown(ctx)
	rec.Shutdown(ctx)
}
```

`rec.Shutdown(ctx)` must run after `srv.Shutdown(ctx)`, so buffered data is
drained after the server has stopped accepting new requests. It is
synchronous: it makes a best-effort attempt to ship whatever is buffered,
bounded by `ctx`.

## Activate

```bash
export MAPING_KEY=your-ingest-key
```

`MAPING_KEY` is the only required configuration. Everything else is
inferred: service name from the binary or `MAPING_SERVICE`, instance from the
hostname, flush timing and sketch parameters from built-in defaults. Without
a key, `NewRecorder` returns a no-op recorder, so adding the middleware to a
codebase is always safe: activation is a matter of setting the environment
variable, decoupled from the code change.

Generate some traffic against the instrumented service, then open the
dashboard. The first flush after startup is accelerated so the first data
point appears within seconds rather than after a full flush window.

Without Gin, call `rec.Observe` directly with a
`maping.Record{Method, RouteTemplate, Status, Duration}` after each request
completes; see the `client` package for the full `Record` shape and the
`Option` functions (`WithEndpoint`, `WithFlushWindow`, `WithDeployVersion`,
and others).

See also: [What data is collected](/doc/data-collected), [Runtime overhead](/doc/runtime-overhead).
