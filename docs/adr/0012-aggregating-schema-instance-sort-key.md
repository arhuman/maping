---
status: accepted
---

# Summaries aggregate-state columns and an instance/method sort key

The data plane stores one `summaries` row per `(tenant, service, instance,
method, route_template, status_class, window)` and rolls it up into 1m/1h/1d
tiers. All tiers use `ENGINE = AggregatingMergeTree`. Adding a per-instance
read query ("is this degradation one replica or fleet-wide?") exposed a latent
correctness bug in that schema.

## The bug

The tables declared the counters as **plain** columns (`count`,
`sum_duration_ns`, `req_bytes`, `resp_bytes` as `UInt64`; `latency_sketch`,
`status_codes` as `Map`) and used
`ORDER BY (tenant, service, route_template, status_class, window_start)` —
which **omits `instance` and `method`**.

`AggregatingMergeTree` collapses rows that share the sorting key, and for a
plain (non-`AggregateFunction`) column it keeps one arbitrary value and
**discards the rest — it does not sum them**. So two replicas of a service that
emit the same `(route_template, status_class, window_start)` — which the rollup
tiers *guarantee* by aligning `window_start` with `toStartOfInterval` — had one
replica's counts silently dropped on merge (or even at insert time, within a
single batched block). This was proven with `OPTIMIZE … FINAL`: a two-replica
fixture lost one replica's 5 requests. It broke per-instance reads and was a
latent **undercount for the existing across-instance aggregates**, contradicting
the schema's own claim that "counters sum … AggregatingMergeTree gives exact
rollups".

## Decision

Make the merge semantics match the intent, and make per-instance rows
first-class:

1. **Aggregate-state columns.** Convert the merge-able columns to
   `SimpleAggregateFunction` so a merge *combines* instead of discards:
   `count`/`sum_duration_ns`/`req_bytes`/`resp_bytes` →
   `SimpleAggregateFunction(sum, UInt64)`; `latency_sketch`/`status_codes` →
   `SimpleAggregateFunction(sumMap, Map(...))`; `window_end` →
   `SimpleAggregateFunction(max, DateTime64(3))`. Reads are unchanged: the query
   layer still `sum()`/`sumMap()`s at read time, and `sumMap`'s result is a plain
   `Map` so `percentileExpr` is unaffected. The writer's plain-value `INSERT`
   is likewise unchanged (a raw value inserts into a `SimpleAggregateFunction`
   column). Verified on ClickHouse 24.8.
2. **Instance/method in the sort key.** Widen every tier to
   `ORDER BY (tenant, service, route_template, status_class, method, instance,
   window_start)` so rows from different replicas/methods never share a key and
   never collapse. The rollup materialized views already `GROUP BY` these
   columns; they now feed the aggregate-state targets and use `max(window_end)`.

## Migration approach — edit the DDL in place, no new migration file

`storage.ApplyMigrations` has **no ledger**: it re-runs every embedded `.sql`
on every startup and depends on each statement being idempotent
(`CREATE … IF NOT EXISTS`). Given that, a new migration file is the wrong tool:

- A `DROP + CREATE` migration would run on **every startup** and wipe data.
- ClickHouse cannot `ALTER` a plain column into a `SimpleAggregateFunction`, nor
  insert key columns into the middle of an existing `ORDER BY`. There is no
  idempotent in-place `ALTER` that achieves this.

So the change edits `0001_summaries.sql` and `0002_rollups.sql` at the source of
truth. Fresh ClickHouse instances get the corrected schema automatically.
**Pre-existing instances keep their old tables** (`IF NOT EXISTS` skips them) and
require a **one-time manual reset** (drop the `summaries*` tables), documented in
a comment block at the top of `0001`. This is acceptable because v0.1.0 is
pre-GA and the ClickHouse data is reconstructable/disposable; the migration
deliberately does **not** automate the drop, so a routine restart can never
delete data.

## Consequences

- Merges now sum instead of discard: the across-instance aggregates are correct,
  and per-instance reads (`InstancesForEndpoint`) are reliable even after
  background merges.
- The sort key is wider, so storage keeps finer-grained rows (one per
  instance/method as well). Cardinality is bounded — replicas × methods is small
  and already governed by the guardrail series cap.
- Existing deployments need the documented one-time reset; there is no automated
  data-preserving upgrade path yet.
- **Follow-up (post-GA):** introduce a run-once migration tool with a
  `schema_migrations` ledger and a data-copy migration (new tables →
  `INSERT … SELECT` → `RENAME` swap → drop old) so future schema changes preserve
  data without a manual reset. Tracked as a known gap.
