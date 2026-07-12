---
status: accepted
---

# ClickHouse for aggregated series storage

The collector stores aggregated Summaries in **ClickHouse**. Each row is
`(tenant, service, instance, endpoint, status-class, window)` plus RED counters and
the latency DDSketch. The DDSketch is stored as a sparse **`Map(Int32, UInt64)`**
(bucket-index → count), matching the wire proto `map<int32, uint64>`. Rollups from 10s → 1min → 1hour → 1day are done by
**`AggregatingMergeTree` + cascading materialized views**: counters merge with
`sum`, sketches merge with `sumMap`. Percentiles are computed at query time from the
merged bucket map. Raw fine-grained tiers expire via `TTL` after rollup.

## Why

The stored unit is not a float sample but counters + a variable-size mergeable
sketch, so Prometheus/VictoriaMetrics/Mimir (fixed-bucket, float-sample models) do
not fit without flattening the sketch into a cardinality explosion. ClickHouse hits
all three performance goals: columnar per-column ZSTD codecs (less disk), columnar
group-by scans + skip indexes (faster retrieval), and native `sumMap` merge of the
sketch buckets (exact, matching ADR-0001, no custom merge code). Multitenancy is a
leading `tenant` column in the primary key with partition pruning.

## Considered and rejected

- **TimescaleDB/Postgres** — simpler to operate, `bytea` sketch works, but
  compression and scan speed lag columnar at scale. Kept as a possible fallback.
- **Prometheus-family TSDBs** — wrong data model for a mergeable sketch.

## Consequences

ClickHouse is heavier to operate than Postgres, but this is provider-side infra,
invisible to customers, so it does not affect the zero-config pillar. Schema, rollup
logic, and the query layer all bind to ClickHouse; swapping it later is costly.
