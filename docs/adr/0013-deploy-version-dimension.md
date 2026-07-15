---
status: accepted
---

# Deploy identity (version / env / region) as a stored dimension

RED metrics and the per-instance / per-status-class breakdowns say *that* an
endpoint degraded and *where* (which replica, which status class), but not
*which release* caused it — the single most common root-cause question. Phase 1
adds a deploy identity so a regression can be attributed to a version.

## Decision

**Carry deploy identity on the `Envelope`, not per series.** The client aggregates
summaries per `(method, route_template, status_class)` within a window and ships
them under one `Envelope`. Deploy identity is a property of the emitting process,
so it belongs on the Envelope (sent once per batch), not on each Summary or the
series key. New additive Envelope fields (backward-compatible; `buf breaking`
enforced):

- `deploy_version` (6) — release version: semver or image tag
- `deploy_id` (7) — git SHA / CI build id (defaults from `debug.ReadBuildInfo`
  `vcs.revision` when unset)
- `environment` (8), `region` (9)
- `instance_start_time_ms` (10) — process boot time, to correlate restarts

The client parses `MAPING_DEPLOY_VERSION/_ID/_ENVIRONMENT/_REGION` (with matching
`With…` options), stamps them on every Envelope, and the server stamps them onto
every stored `Row` from that batch.

**Store as low-cardinality columns; put `deploy_version` in the ClickHouse sort
key.** The four string fields are `LowCardinality(String)`; `instance_start_time`
is `SimpleAggregateFunction(max, DateTime64(3))`. `deploy_version` is added to the
`ORDER BY` of every tier (before `window_start`) so rows from different releases
never collapse-and-sum under `AggregatingMergeTree` (the same class of bug fixed
in ADR-0012) — correct per-version aggregation depends on it. `environment`,
`region`, and `deploy_id` remain non-key stored columns (constant per series in
practice). `VersionsForEndpoint` exposes per-version RED, mirroring
`InstancesForEndpoint`.

## Why version is NOT in the cardinality series key

The per-tenant series-cardinality budget (client `seriesKey` and
`guardrail.SeriesKey`) guards the *structural API surface*
(`method|route_template|status_class`). Folding `deploy_version` into it would let
routine deploy churn consume a tenant's budget and freeze new series. Version
therefore lives on the Envelope and in the storage sort key (a separate concern
from the tenant budget), never in the cardinality key.

## Migration

The DDL is edited in place in `0001_summaries.sql` / `0002_rollups.sql`, for the
same reasons as ADR-0012 (the migration runner is ledger-less, and ClickHouse
cannot `ALTER` a column into `LowCardinality`/`SimpleAggregateFunction` nor insert
a key column into an existing `ORDER BY`). Fresh instances get the schema
automatically; pre-existing instances need the documented one-time hand-drop of
the `summaries*` tables. The rollup materialized views carry the four columns in
`SELECT`/`GROUP BY` and `max(instance_start_time)`.

## Consequences

- Regressions can be attributed to a `deploy_version` (and sliced by
  `environment`/`region`); a bad release shows a distinct error/latency profile
  next to the prior version.
- The sort key is wider, but bounded — only a handful of releases are live at once,
  and stale versions age out with the window/TTL.
- Existing deployments need the one-time reset (no data-preserving upgrade yet;
  see the ADR-0012 follow-up on a run-once migration tool).
- **UI is a follow-up**: a per-version panel on the endpoint-detail page and
  deploy-marker overlays on the time-series chart are not part of this change; the
  data and `VersionsForEndpoint` query land first.
