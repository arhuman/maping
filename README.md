# mAPI-ng

**mAPI-ng** (monitored API, NextGen): evidence-backed incident diagnosis for Go APIs.

Open source. Start free on the [hosted service](https://www.mapi-ng.com) (no card), or self-host the complete MIT stack.

mAPI-ng answers one question: my endpoint got slower or started failing, what is the most
likely cause, and what should I check next? It correlates RED metrics with Go runtime,
instance, deployment, and downstream signals, then ranks the most plausible causes and shows
the evidence behind each. Every cause carries a
"Rules this out:" line so you can falsify it, and when nothing explains the anomaly it says
"Unattributed" rather than inventing a cause.

Instrument in a few lines: two imports, construct a recorder, add one middleware, set one env
var. An absent key keeps the middleware inactive and prevents data transmission, so
instrumentation can be merged independently from activation.

![Endpoint-detail diagnosis card: "Memory / GC pressure, High (3/4 signals)" ranking a
worsening memory leak from correlated evidence (post-GC heap climbing 3.4x, allocation rate
835x baseline, GC frequency 111x baseline), with a rising heap chart and a falsifier line that
says what would rule the cause out.](docs/img/diagnosis-card.png)

---

## Three principles

**Diagnosis.** mAPI-ng interprets signals instead of adding another wall of
graphs. On the endpoint-detail page it ranks 8 cause families (memory and GC, CPU,
connection and pool congestion, overload and timeouts, goroutine leak, downstream and IO,
instance-localized, release regression), each with its evidence and what would rule it out.
Confidence is a discrete tier (High, Medium, Low), never a percentage.

**Simplicity.** Go-specialized. One recorder, one middleware, one
required input (`MAPING_KEY`); everything else is inferred: service name from the binary,
instance from the hostname, flush timing and sketch parameters from defaults. No Prometheus,
Grafana, or YAML to operate.

**Efficiency.** The client aggregates per-endpoint metrics in-process into a
DDSketch before sending. The server stores compact Summaries in ClickHouse and rolls them up
through 1-minute, 1-hour, and 1-day tiers. In-process aggregation reduces transmitted, stored,
and queried data compared with raw-event collection.

---

## Positioning

mAPI-ng targets the onboarding, interpretation, and operational cost pain of the OTel +
Prometheus + Grafana stack, not its flexibility. It does not replace full distributed tracing
or custom dashboards. It gives you RED metrics (rate, errors, duration) and a ranked,
evidence-backed diagnosis for every Go HTTP endpoint, from a few lines of instrumentation.

For the full product framing, design decisions, and terminology, see [`docs/context.md`](docs/context.md).

---

## Getting started

**Run the collector and dashboard (one command):**

```bash
make local
```

Creates `.env` for you, builds the server, starts ClickHouse and Postgres, and applies the
schema at boot. Open the dashboard at http://localhost:8080. Auth is off by default; Postgres
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

**Instrument a Go service:**

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

**Full details:** [`client/README.md`](client/README.md), [`server/README.md`](server/README.md), [`proto/README.md`](proto/README.md)

---

## Hosted or self-hosted

Everything in this repository is MIT and runs on your own infrastructure with `make local`
(dev) or `make up` (prod). Nothing here is held back to push you toward the hosted service.

If you would rather not operate ClickHouse and Postgres yourself,
**[mAPI-ng Cloud](https://www.mapi-ng.com)** runs this same stack for you, with a forever-free
tier and no card required. Your instrumentation is byte-for-byte identical either way (only
`MAPING_KEY` changes), so you can start hosted and move to self-hosting later, or the reverse,
at any time. Convenience, not lock-in.

---

## Repo layout

```
mAPI-ng/
  client/          Go module: github.com/arhuman/maping/client      (MIT)
    gin/           Go module: github.com/arhuman/maping/client/gin  (MIT, Gin adapter)
  server/          Go module: github.com/arhuman/maping/server      (MIT)
    cmd/maping-server/
    internal/
  proto/           Go module: github.com/arhuman/maping/proto       (MIT, shared protobuf)
  example/         Go module: github.com/arhuman/maping/example     (MIT, runnable quickstart)
  Dockerfile       maping-server image (Go workspace build)
  docker-compose*.yml  neutral base + local/prod overlays
  env.sample       template for .env (make local / make up read it)
  LICENSE          MIT license (applies to the whole repository)
  docs/context.md  Product framing, design decisions, terminology
  docs/adr/        Architecture Decision Records
  go.work          Workspace tying proto, client, client/gin, server, and example together
```

The repository is an **open-source, multi-module** project: every module (`proto`, `client`,
the framework adapters, and `server`) is **MIT** (auditable, safe to import into production hot
paths, free to self-host and modify). Keeping the Gin adapter in a separate module means importing
the core client never pulls Gin into your binary.

The modules are wired together for local development by `go.work`, so build and test from the
repo root (or anywhere inside the tree). The workspace resolves cross-module imports from
source. Published tags are cut with `make release VERSION=vX.Y.Z`, which pins each module to
the real released versions of its siblings (no local `replace` directives leak to consumers).

---

## Documentation

User-facing documentation (quickstart, what data is collected, runtime overhead, failure
and retry behaviour, security and data flow, self-hosting, architecture, benchmarks, and
licensing) lives in [`server/docs/content/`](server/docs/content/) and is served by the
running server at **`/doc`** (public, no configuration, community build included). Deeper
design rationale lives in the [Architecture Decision Records](docs/adr/).

---

## Architecture Decision Records

Design decisions are recorded one file per decision under [`docs/adr/`](docs/adr/); see the
[ADR index](docs/adr/README.md) for the full list, including the diagnosis engine
([0021](docs/adr/0021-diagnosis-engine.md)) and the MIT relicense
([0022](docs/adr/0022-relicense-server-mit.md)).

---

## License

mAPI-ng is released under the **MIT License**: the whole repository, every module
(`proto`, `client` and its adapters, `server`, `example`).

Each module directory carries an MIT `LICENSE` file for tooling; the root `LICENSE` is the
same MIT license and governs the repository. See [ADR-0022](docs/adr/0022-relicense-server-mit.md)
for why the server moved from BSL 1.1 to MIT.

---

## Development

Key `Makefile` targets for contributors:
- `make test`: Run all tests.
- `make audit`: Run linting and vulnerability checks.
- `make tidy`: Format code and tidy all `go.mod` files.

For more detailed guidelines on submitting changes, please see [`CONTRIBUTING.md`](CONTRIBUTING.md).