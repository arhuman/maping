# mAPI-ng — Context Glossary

> **mAPI-ng** (monitored API, NextGen): evidence-backed incident diagnosis for Go
> services. Three principles: **Diagnosis** (the value: correlate RED, Go runtime,
> per-instance USE, memory, downstream, deployment and version signals as a pure
> function over data the page already loads, then rank the likely causes with the
> evidence behind each and a falsifier, or answer "Unattributed" rather than invent
> one), **Simplicity** (Go-specialized, zero-config, no Prometheus/Grafana to set
> up), and **Efficiency** (DDSketch local aggregation, compact summaries, time-range
> rollups: more data/s ingested, less disk, faster retrieval). Available as a hosted
> service (free tier, no card) or fully self-hosted from the MIT stack. Positioned
> against OpenTelemetry+Prometheus+Grafana on onboarding UX, interpretation, and
> cost/performance, not on flexibility; it does not replace distributed tracing or
> custom dashboards.

## Terms

**Summary** — the unit of data mAPI-ng ships. A compact, client-side aggregate
covering one flush window, keyed by `(endpoint, status-class)`: request `count`
(→rate), the latency **DDSketch**, `sum_duration` (for an exact mean), a bounded
`status_code_breakdown`, and **request/response byte sums** (→bandwidth per
endpoint). Error counts are *derived* from status classes at query time, not stored
separately. Uploads are wrapped in an envelope carrying `service`, `instance`,
`sdk_version`, and `dropped_summaries` (so client-side backpressure loss is
*visible*). mAPI-ng does *not* ship per-request events in v1.

**Excluded from v1** (confirmed out): per-request events, caller identity, user/
custom tags, in-flight/concurrency gauges. Each would add cardinality or schema cost
without serving the launch dashboard.

**Flush window** — the time interval (default 10s) over which the client accumulates a
Summary in-process before sending it. Chosen so a busy service sends one small
Summary per window instead of one record per request. Window start/end are
**client-stamped** (client wall clock); latency **durations** are derived from a
**monotonic** clock so NTP steps can't produce absurd values. The server treats
client timestamps as authoritative only within a tolerance band and **drops** out-of-band
(skewed/malicious) Summaries, counting them into `RejectedSummaries` (surfaced in the
dashboard) rather than clamping them onto now. In-band drift is kept as-is.

**Distribution shape** — the queryable output for v1: rate, error rate, and latency
percentiles (p50/p95/p99) over time. Individual per-request forensics are
explicitly out of scope for v1.

**Series key** — the tuple that identifies one time series. v1 is closed and
fully auto-derived (no user-supplied labels):
`(service, instance, method, route-template, status-class)`.

**Source** — a monitored service *instance*. Composed of **service** (logical app
name, e.g. `checkout-api`, auto-derived from env/binary) and **instance** (a
specific replica, auto-derived from hostname/pod). The server can merge across
instances of the same service at query time.

**Endpoint** — `method + route-template` (e.g. `GET /users/:id`). Always the
registered route template, never the raw path — mAPI-ng never emits raw paths, so
per-value paths cannot explode cardinality. On Gin, derived from `c.FullPath()`.

**Status-class** — the HTTP status bucketed to `2xx/3xx/4xx/5xx` for the series
key. Exact codes live only inside the error breakdown, not as series labels.

**Tenant** — the unit of multitenant isolation and data ownership; **a tenant is one
organization** (a team). Human **members** belong to it and all see the same
services/data. A solo user is just an org of one (avoids a later migration). Never
configured client-side: the **Ingest key** encodes the tenant, and the server
resolves it from the key.

**Member / roles** — humans in an organization, authenticated via **OIDC only
(GitHub/Google), no passwords** in v1. Two roles: **admin** (manage keys and
members) and **member** (read dashboards). Fuller RBAC and SAML/SSO are out of
scope for v1.

**Ingest key** — a secret a client needs. Delivered via `MAPING_KEY` env var (or in
code). Encodes the tenant; it is the *only* required client configuration. A tenant
can have **many labeled keys** (e.g. `checkout-prod`, `checkout-staging`), each
independently revocable/rotatable — keys are a distribution/rotation convenience, all
resolving to the same org's data pool, **not** an isolation boundary between them.

**Handshake** — a one-time registration ping sent on recorder startup with a valid
key (carrying `service`, `instance`, `sdk_version`) — *not* a metric. Proves auth +
connectivity instantly, before any traffic, and drives the dashboard's live 4-step
onboarding state (key valid → service connected → waiting for first Summary → first
data received). The **first flush is accelerated** on cold start so first data
arrives in seconds, then reverts to the normal Flush window. Setup failures (bad key,
clock skew, cardinality frozen, network/TLS) are **loud in the dashboard** and
rate-limited in host logs — data fails open (Q7), but setup problems are never
silent.

**Zero-config** — the simplicity contract: one input (`MAPING_KEY`), everything
else inferred (endpoint defaults to the hosted collector; service/instance/flush/
sketch params auto-derived). **Absent key ⇒ the middleware is a no-op**, so adding
mAPI-ng to a codebase is always safe and activation is decoupled from the code
change (flip an env var to turn it on).

**Sketch** — the latency structure inside a Summary: a **DDSketch** (γ=1.01,
range [1µs, 60s]). Mergeable (exact, associative) and percentile-queryable with a
provable ~0.5% relative-error bound ((γ−1)/(γ+1) ≈ 0.4975% at γ=1.01). Same structure on the wire and at rest; stored as
a sparse `Map(bucket-index → count)`. See ADR-0001, ADR-0003.

**Core** — the framework-agnostic client package (`maping`). Owns config/env
parsing, the Summary + DDSketch, the sharded hot-path recorder, the buffer, the
Connect uploader, and guardrail/backoff handling. Its input is a neutral
`Record{Method, RouteTemplate, StatusClass, Duration}`. Written once.

**Adapter** — a thin per-framework shim (`maping/gin` in v1). Its only job is to
extract two things after the request completes — the route template and the final
status code — and call the Core's `Observe(record)`. ~20 lines. Only the adapter
imports the framework, so a non-Gin user never pulls Gin into their binary.

**Control plane** — the relational store (Postgres) of tenants, ingest keys, users,
plans, and per-tenant limits. Small, transactional. Ingest validates keys against it
(cached) and resolves the tenant.

**Data plane** — the Summaries store (ClickHouse). Row-level multitenant: every row
is stamped with the server-resolved tenant. There is no infrastructure wall between
tenants, so all isolation is software-enforced.

**Language-agnostic server** — the collector, wire contract (protobuf), and storage
schema contain no Go-isms; "Go-specialized" describes client DX and how the product is
presented, not the server. Go is the sole v1 client, but a polyglot client is a cheap future
expansion needing no server change. Go-only fields (if ever added) go in an optional
extension block, never the shared metrics schema.

**Guardrails** — hard, per-tenant, server-enforced limits (the client is untrusted):
series-cardinality cap, ingest rate limit, max payload size, retention — all
defaulted by plan. On cardinality cap: **freeze new series**, keep existing ones,
surface the limit in the dashboard. On over-limit: server fails *closed* with a
reason code (unlike the client, which fails open).

**Error** — for the headline error rate, a request counts as an error if it is
**4xx, 5xx, a panic, or a write-timeout**. Panics are recorded as 5xx *without
altering host behavior* (observed, then re-panicked — a Go-specialized advantage of
owning the middleware). Write-timeouts / aborted-before-status are counted as errors
under a distinct no-status class. The dashboard always shows the
4xx/5xx/timeout breakdown alongside the headline so operators can see *why* the rate
is high (raw 4xx includes bot 404s and expired-token 401s).

**Dashboard**: a fixed, non-configurable, auto-generated 3-level view (service
overview → endpoint table → endpoint detail), served by mAPI-ng's own
ClickHouse-backed web app. The endpoint-detail tier is the diagnosis surface: a
latency histogram rendered from the DDSketch, per-instance USE gauges, the memory
trend, the downstream split, and the ranked-cause **diagnosis card**. No custom
panels, no query builder, no user dashboards in v1. Alerting is deferred to v2.

**Diagnosis** (ADR-0021): the endpoint-detail engine that correlates the page's
signals (RED, USE, memory, downstream, versions, instances) and ranks 8 cause
families: memory/GC, CPU, connection/pool congestion, overload/timeouts, goroutine
leak, downstream/IO, instance-localized, and release regression. It is a pure
function over data the page already loads. Each ranked cause carries evidence
bullets and a falsifier (shown as "Rules this out:"); confidence is a discrete tier
(High/Medium/Low, e.g. "High (3/4 signals)"), never a percentage. When the endpoint
is anomalous but no rule fires, the top cause is "Unattributed", so the card never
invents a cause.

**Rollup** — server-side aggregation of Summaries into coarser time tiers
(10s → 1min → 1hour → 1day). Counters sum; sketches merge bucket-wise. Fine tiers
expire after rollup to save disk. Mergeability is what makes this exact.
