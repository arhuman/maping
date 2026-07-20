# Architecture

mAPI-ng splits into a **control plane** and a **data plane**, plus a
server-rendered dashboard that reads from both.

## Control plane: Postgres

Holds tenants, ingest keys, members, and per-tenant plan limits. Small and
transactional. The ingest handler validates keys against it (cached) and
resolves the tenant for every request. Dashboard authentication (OIDC,
sessions) is wired only when a control plane is configured; without it the
server runs single-tenant with no login. See
[Security & data flow](/doc/security-data-flow) for the auth details.

## Data plane: ClickHouse

Holds Summaries: one row per `(tenant, service, instance, endpoint,
status-class, window)`, with RED counters and a latency DDSketch. Every row
carries a server-resolved tenant column; there is no infrastructure-level
wall between tenants, so isolation is enforced entirely by the query layer
(see [Security & data flow](/doc/security-data-flow)). ClickHouse was chosen
for its columnar storage (better compression for this shape than a
row-store) and its native `sumMap` merge, which matches the DDSketch's exact
associative-merge property with no custom merge code. See ADR-0003 in the
repository's `docs/adr/` for the full reasoning.

## DDSketch aggregation

Latency is aggregated client-side into a **DDSketch** (relative-error bound
of about 0.5%, value range clamped to `[1us, 60s]`), the single latency
structure used both on the wire and at rest. DDSketch is mergeable and
exact: instances can be merged into a service view, and 10-second windows
can be merged into coarser rollups, without accumulating error. See
ADR-0001 in the repository for why this was chosen over fixed histogram
buckets or t-digest.

## Rollups and retention

Summaries are rolled up from 10-second windows into 1-minute, 1-hour, and
1-day tiers by ClickHouse materialized views: counters sum, sketches merge
bucket-wise. Fine-grained tiers expire after they have been rolled up into
the next tier, so raw 10-second data does not accumulate indefinitely while
coarser tiers remain queryable further back in time. Percentiles are
computed at query time from the merged sketch, never stored pre-computed.

## Dashboard

The dashboard is a fixed, non-configurable, auto-generated three-level RED
view: service overview, endpoint table, endpoint detail with a latency
histogram rendered from the DDSketch. It is server-rendered Go
(`html/template`) plus htmx for interaction and inline SVG for charts; there
is no client-side build step and, outside one small self-hosted
copy-to-clipboard helper, no client-side JavaScript. Navigation, sorting,
and time-window switching are plain links and query parameters. This keeps
the dashboard's Content-Security-Policy tight (`script-src 'self'`) and
means the entire product ships as a single Go binary with embedded assets.

## Per-instance resource gauges

Alongside RED Summaries, each flush window also carries one per-instance
snapshot of process health (CPU time, RSS, heap, GC pause time,
goroutines), stored in its own table rather than folded into the endpoint
series. This answers "did the machine itself degrade" (GC pressure,
goroutine growth) separately from "did a specific endpoint get slower",
without adding a process-level dimension to the endpoint series key.

For the underlying design decisions, see the repository's `docs/adr/`
directory; this page summarizes the shape without repeating that record.
