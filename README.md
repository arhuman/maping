# mAPI-ng

**mAPI-ng** (monitored API, NextGen) — hosted, zero-config API observability for Go services.

One env var, one middleware call, and your service reports rate, error rate, and latency
percentiles to a hosted dashboard — no Prometheus, no Grafana, no YAML to write.

---

## Two pillars

**Simplicity.** Go-specialized. One required input (`MAPING_KEY`). Everything else is
inferred: service name from the binary, instance from the hostname, flush timing and sketch
parameters from defaults. An absent key makes the middleware a no-op, so adding mAPI-ng to
a codebase is always safe and activation is decoupled from the code change.

**Performance.** The client aggregates per-endpoint metrics in-process into a DDSketch
before sending. The server stores compact Summaries in ClickHouse and rolls them up through
1-minute, 1-hour, and 1-day tiers. More data per second ingested, less disk used, faster
queries compared to raw-event pipelines.

---

## Positioning

mAPI-ng targets the onboarding and operational cost pain of the OTel + Prometheus + Grafana
stack, not its flexibility. It does not replace full distributed tracing or custom dashboards.
It gives you RED metrics (rate, errors, duration) for every Go HTTP endpoint in the time it
takes to set one env var.

For the full product framing, design decisions, and terminology, see [`docs/context.md`](docs/context.md).

---

## Repo layout

```
mAPI-ng/
  client/          Go module: github.com/arhuman/maping/client      (MIT)
    gin/           Go module: github.com/arhuman/maping/client/gin  (MIT, Gin adapter)
  server/          Go module: github.com/arhuman/maping/server      (BSL 1.1)
    cmd/maping-server/
    internal/
  proto/           Go module: github.com/arhuman/maping/proto       (MIT, shared protobuf)
  example/         Go module: github.com/arhuman/maping/example     (MIT, runnable quickstart)
  Dockerfile       maping-server image (Go workspace build)
  docker-compose*.yml  neutral base + local/prod overlays
  env.sample       template for .env (make local / make up read it)
  LICENSE          per-module license map (this repo is not single-licensed)
  docs/context.md  Product framing, design decisions, terminology
  docs/adr/        Architecture Decision Records
  go.work          Workspace tying proto, client, client/gin, server, and example together
```

The repository is an **open-core, multi-module** project: `proto`, `client`, and the
`client/gin` adapter are **MIT** (auditable, safe to import into production hot paths);
`server` is **BSL 1.1** (source-available, self-hostable for non-competing use). Each module
carries its own `LICENSE`; the root `LICENSE` is only the map. Keeping the Gin adapter in a
separate module means importing the core client never pulls Gin into your binary.

The modules are wired together for local development by `go.work`, so build and test from the
repo root (or anywhere inside the tree) — the workspace resolves cross-module imports from
source. Published tags are cut with `make release VERSION=vX.Y.Z`, which pins each module to
the real released versions of its siblings (no local `replace` directives leak to consumers).

---

## Getting started

**Run the collector and dashboard (one command):**

```bash
make local
```

Creates `.env` for you, builds the server, starts ClickHouse and Postgres, and applies the
schema at boot. Open the dashboard at http://localhost:8080. Auth is off by default — Postgres
stays unused until you set `MAPING_POSTGRES_DSN`. No manual migration step needed.

**Fill the dashboard with sample data:**

```bash
make generate-traffic            # tune volume with ROUNDS, e.g. make generate-traffic ROUNDS=30
```

Builds and runs the `example/` service against the running local stack, fires one request across
every route (2xx, labelled 5xx, aborted, and a downstream call), waits for a flush, then stops it.
The collector endpoint is pinned to `http://127.0.0.1:$MAPING_PORT`, so this only ever enriches the
local server, never a remote collector. Reload the dashboard to see the `example-api` service with
its endpoints, error classes, no-status reasons, downstream split, and per-instance USE gauges.

**Instrument a Go service (three lines):**

```bash
go get github.com/arhuman/maping/client       # core recorder (no web-framework deps)
go get github.com/arhuman/maping/client/gin    # optional Gin adapter (only if you use Gin)
```

```go
import (
	maping "github.com/arhuman/maping/client"
	mapinggin "github.com/arhuman/maping/client/gin"
)

rec := maping.NewRecorder(maping.WithService("my-api"))
r.Use(mapinggin.MiddlewareWithRecorder(rec)) // above gin.Recovery()
```

```bash
export MAPING_KEY=your-ingest-key   # the only required input; absent = no-op
```

**Full details:** [`client/README.md`](client/README.md) — [`server/README.md`](server/README.md) — [`proto/`](proto/)

---

## Architecture Decision Records

Design decisions are recorded in [`docs/adr/`](docs/adr/):

| ADR | Title |
|---|---|
| [0001](docs/adr/0001-ddsketch-for-latency.md) | DDSketch for latency aggregation |
| [0002](docs/adr/0002-connect-client-grpc-server.md) | Connect client / gRPC server |
| [0003](docs/adr/0003-clickhouse-storage.md) | ClickHouse for storage |
| [0004](docs/adr/0004-open-core-licensing.md) | Open-core: MIT client, BSL server |
| [0005](docs/adr/0005-ingest-direct-then-queue.md) | Direct batched ClickHouse writes for v1, durable queue later |
| [0006](docs/adr/0006-dashboard-server-rendered-htmx-uplot.md) | Dashboard: server-rendered Go + htmx + uPlot (superseded by 0008) |
| [0007](docs/adr/0007-dashboard-auth-oidc-session-cookies.md) | Dashboard auth: OIDC, stateless session cookies |
| [0008](docs/adr/0008-dashboard-js-budget-csp.md) | Dashboard JS budget and Content-Security-Policy |
| [0009](docs/adr/0009-setup-form-csrf-synchronizer-token.md) | Setup form CSRF: stateless HMAC synchronizer token |
| [0010](docs/adr/0010-tenant-scoped-queries.md) | Tenant-scoped data-plane access (un-scoped query unrepresentable) |
| [0011](docs/adr/0011-ci-quality-gate.md) | CI quality gate: run the Makefile targets on push/PR |


---

## License

This repository is not single-licensed. Each module has its own license.

| Module | License |
|---|---|
| `proto` | MIT |
| `client` | MIT |
| `client/gin` | MIT |
| `server` | Business Source License 1.1 |
| `example` | MIT |

See the `LICENSE` file in each module directory for the full text. The root `LICENSE` file
is only a map of the licenses used.

---

## Development

This repository is a [Go workspace](https://go.dev/doc/tutorial/workspaces), which means you can work on all modules (`proto`, `client`, `server`, etc.) simultaneously. The `go.work` file at the root handles the build.

To get started, you only need to run `make local` to stand up the full development stack (server, ClickHouse, Postgres).

Key `Makefile` targets for contributors:
- `make test`: Run all tests.
- `make audit`: Run linting and vulnerability checks.
- `make tidy`: Format code and tidy all `go.mod` files.

For more detailed guidelines on submitting changes, please see [`CONTRIBUTING.md`](CONTRIBUTING.md).