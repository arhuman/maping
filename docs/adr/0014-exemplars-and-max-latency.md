---
status: accepted
---

# Exemplars: bounded request breadcrumbs from an aggregate to a trace

RED metrics and the instance/version/status-class breakdowns tell you *that* an
endpoint degraded, *which* replica/release, and *what* class of error — but not
*which actual request* to open in a tracing tool or log search. mAPI-ng stores no
per-request events by design (ADR-0001/0003), so the aggregate is a wall: you see
a p99 or error spike with nothing to click through to.

## Decision

Attach a **bounded sample of exemplars** and the **exact max latency** to each
per-window series aggregate — breadcrumbs that point *outward* to the user's
tracing/log system, without turning mAPI-ng into an event store.

- **`max_duration_ns`** (Summary field 12): the exact slowest request in the
  window. Stored `SimpleAggregateFunction(max, UInt64)` so it merges correctly and
  rolls up via `max()` across all tiers.
- **`exemplars`** (Summary field 13, repeated `Exemplar{at, duration_ns,
  status_code, trace_id, span_id, request_id}`): a **K=3** reservoir per series per
  window — the single slowest request plus the first two error requests
  (4xx/5xx/no_status). Updated O(1) and allocation-free on the client hot path.
- **Client capture is dependency-free and best-effort.** The Gin adapter parses
  the W3C `traceparent` request header into `trace_id`/`span_id` and reads
  `X-Request-Id` into `request_id`; all optional, empty when absent. No OpenTelemetry
  dependency is added to the client.

## Storage: raw tier only, best-effort

`exemplars` is an `Array(Tuple(DateTime64(3), UInt64, UInt32, String, String,
String))` on the **raw `summaries` table only** — it is deliberately NOT carried
into the 1m/1h/1d rollup tables or their materialized views, and it ages out under
the raw tier's short TTL. You debug against recent, fine-grained data; keeping
exemplars only there bounds storage and avoids rollup bloat. `ExemplarsForEndpoint`
reads the raw tier, `ARRAY JOIN`s the exemplars, orders by duration desc, and
returns a small top-N.

Exemplars are a **best-effort sample**, not exact data. They are stored as a plain
array (not a mergeable aggregate state), so an `AggregatingMergeTree` collapse of
same-key rows may drop some — which is acceptable for a sample of breadcrumbs and
avoids the complexity of an exactly-mergeable exemplar type.

## Why not in the series / cardinality key

`max_duration_ns` and `exemplars` are per-series *aggregate fields*, not
dimensions. The client `seriesKey` and the `guardrail` cardinality key are
unchanged — exemplars never widen a tenant's series budget.

## Migration

DDL edited in place in `0001_summaries.sql` / `0002_rollups.sql` (ledger-less
runner; ClickHouse cannot `ALTER` a plain column into these types), with the same
one-time-reset requirement for pre-existing instances documented in ADR-0012/0013.

## Consequences

- From a p99/error spike, a user gets a real `trace_id`/`request_id` to open in
  Jaeger/Tempo/logs — the missing link from aggregate to specifics.
- Cost stays bounded: K=3 per window client-side, raw-tier-only server-side, short
  TTL.
- Some exemplars may be lost to same-key merges (best-effort by design).
- **UI is a follow-up**: an exemplars panel on the endpoint-detail page (with an
  optional deep-link to a configured trace tool) and a max-vs-p99 outlier flag are
  not part of this change; the data and `ExemplarsForEndpoint` query land first.
