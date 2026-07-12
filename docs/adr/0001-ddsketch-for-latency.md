---
status: accepted
---

# DDSketch for latency aggregation

mAPI-ng aggregates request latency client-side into a mergeable sketch rather than
shipping per-request events or fixed histogram buckets. We use **DDSketch**
(γ=1.01, ≈0.5% relative-error bound — the DDSketch guarantee is (γ−1)/(γ+1) ≈ 0.4975%
at γ=1.01; the familiar ~1% figure corresponds to γ=1.02 — value range clamped to
[1µs, 60s], sparse storage)
as the single latency structure both on the wire and at rest.

## Why

The sketch must survive two operations everywhere in the pipeline: **merge**
(instances→service, 10s windows→minute→hour) and **percentile query**
(p50/p95/p99). DDSketch is the only candidate that gives both a *provable relative
-error bound* (so it is accurate with zero tuning across services whose latencies
span microseconds to tens of seconds — essential for the zero-config pillar) and an
*exact, associative merge* (so rollups don't accumulate error).

## Considered and rejected

- **Fixed Prometheus-style buckets** — merge is trivial but accuracy depends on
  hand-picked boundaries. That directly contradicts zero-config: no fixed boundary
  set is accurate across all services' latency ranges.
- **t-digest** — good tail accuracy and compact, but its merge is lossy and
  order-dependent, which silently corrupts the instance/time rollups that are core
  to our storage model.

## Consequences

Sketch size grows with the ratio of max/min observed latency; the [1µs, 60s] clamp
bounds it to a few hundred buckets, stored sparsely so typically far fewer. This
structure is frozen into both the wire protocol and the on-disk format, so changing
it later is a breaking migration.
