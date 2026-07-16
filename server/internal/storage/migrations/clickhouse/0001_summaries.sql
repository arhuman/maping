-- 0001_summaries.sql — raw fine-grained Summary tier for the mAPI-ng data plane.
--
-- One row is one client-side aggregate for a single (endpoint, status-class)
-- over one flush window, stamped with the server-resolved tenant. Counters sum
-- and the latency DDSketch merges bucket-wise (sumMap) at query and rollup time,
-- so AggregatingMergeTree gives exact rollups without custom merge code
-- (ADR-0003).
--
-- The sketch is a sparse Map(Int32,UInt64): bucket index -> count. Int32 keys
-- match the proto (map<int32,uint64> latency_sketch) and avoid a later
-- migration. The gamma/offset bucket layout is a FROZEN contract shared with the
-- client and with query.go's percentile SQL (ADR-0001).
--
-- status_class is stored as Enum8 matching the proto StatusClass enum so the
-- stored values stay self-describing and 1:1 with the wire contract.
--
-- BREAKING SCHEMA CHANGE (v0.1.0, pre-GA, disposable data)
-- ---------------------------------------------------------------------------
-- The aggregate columns are SimpleAggregateFunction(sum/sumMap/max, ...) and the
-- ORDER BY now includes method and instance. Earlier releases used PLAIN columns
-- with an ORDER BY that excluded instance and method, so rows sharing
-- (tenant, service, route_template, status_class, window_start) COLLAPSED on
-- merge and the duplicates were DISCARDED, not summed — silently losing one
-- instance's (or one method's) data. The SAF column types make the engine SUM on
-- collapse; the wider sort key stops per-instance / per-method rows collapsing at
-- all.
--
-- DEPLOY DIMENSION (v0.1.0, pre-GA, disposable data)
-- ---------------------------------------------------------------------------
-- deploy_version / deploy_id / environment / region are LowCardinality(String)
-- stored dimensions carried from the Envelope, and instance_start_time is a
-- SimpleAggregateFunction(max, DateTime64(3)) correlating restarts. deploy_version
-- is ALSO added to the ORDER BY (right before window_start) so rows from different
-- releases never collapse/sum together — this is what makes per-version RED
-- aggregation correct. environment / region / deploy_id stay non-key stored
-- columns. The non-key columns backfill automatically (see the upgrade note and
-- ALTERs below); only deploy_version's ORDER BY placement needs the one-time drop.
--
-- EXEMPLARS + MAX DURATION (v0.1.0, pre-GA, disposable data)
-- ---------------------------------------------------------------------------
-- max_duration_ns is a SimpleAggregateFunction(max, UInt64): the exact slowest
-- request in the window, merged with max on collapse and across all rollup tiers.
-- exemplars is a SimpleAggregateFunction(groupArrayArray, Array(Tuple(
-- DateTime64(3), UInt64, UInt32, String, String, String))) = (at, duration_ns,
-- status_code, trace_id, span_id, request_id): a bounded, best-effort sample of
-- real requests used to pivot from a spike to an actual request. groupArrayArray
-- CONCATENATES the arrays when same-key rows collapse — a plain Array column on
-- an AggregatingMergeTree keeps one arbitrary row's value on collapse, silently
-- dropping the other rows' exemplars (found by TestExemplarsForEndpoint). Growth
-- stays bounded: each inserted row carries a client-capped sample, collapse only
-- concatenates rows of the SAME series key and window, and reads are capped by
-- maxExemplarsPerEndpoint. Exemplars live ONLY on this raw tier (under the 7-day
-- TTL); the rollup tables and MVs deliberately do NOT carry them. Both are
-- backfilled automatically via the ALTERs below (no drop needed).
--
-- DOWNSTREAM TIME (v0.1.0, pre-GA, disposable data)
-- ---------------------------------------------------------------------------
-- sum_downstream_duration_ns is a SimpleAggregateFunction(sum, UInt64): the
-- summed time requests spent waiting on downstream calls (outbound HTTP), so the
-- endpoint's own time can be split from time blocked on a dependency. It sums on
-- collapse and rolls up across all tiers, and backfills automatically (no drop).
--
-- ERROR CLASS + NO-STATUS REASONS (v0.1.0, pre-GA, disposable data)
-- ---------------------------------------------------------------------------
-- error_classes is a SimpleAggregateFunction(sumMap, Map(String,UInt64)) of
-- bounded, normalized error labels ("DB_POOL_EXHAUSTED"), so a 5xx spike can be
-- attributed to a cause. no_status_reasons is a
-- SimpleAggregateFunction(sumMap, Map(UInt8,UInt64)) keyed by the proto
-- NoStatusReason enum value, telling apart timing-out vs canceling vs crashing
-- requests. Both merge with sumMap on collapse and roll up across all tiers
-- (unlike exemplars), and backfill automatically via the ALTERs below (no drop).
--
-- UPGRADE PATH — automatic column backfill
-- ---------------------------------------------------------------------------
-- ApplyMigrations re-runs every .sql on startup. CREATE ... IF NOT EXISTS builds
-- the full schema on a FRESH install but will NOT alter a table that already
-- exists, so every column added after the original schema is ALSO backfilled
-- below with ALTER TABLE ... ADD COLUMN IF NOT EXISTS. Those run on every boot and
-- are idempotent no-ops once the column is present, so a pre-existing instance is
-- upgraded in place — no manual step, no data loss. The ALTERs deliberately
-- precede the materialized views in 0002 (whose SELECT reads these columns), so an
-- upgrade never hits an "unknown column" error while (re)creating an MV.
--
-- The ONE change ALTER cannot make is to a table's sort key: deploy_version was
-- added to the ORDER BY, and the original data-loss fix widened it to include
-- instance/method. An instance predating THOSE sort-key changes still needs a
-- one-time drop (pre-GA, disposable data) so the tables recreate with the correct
-- ORDER BY — but column-only upgrades no longer do:
--
--   DROP TABLE IF EXISTS summaries, summaries_1m, summaries_1h, summaries_1d;
--   DROP VIEW  IF EXISTS summaries_1m_mv, summaries_1h_mv, summaries_1d_mv;

CREATE TABLE IF NOT EXISTS summaries
(
    tenant          String,
    service         String,
    instance        String,
    method          String,
    route_template  String,
    status_class    Enum8(
        'STATUS_CLASS_UNSPECIFIED' = 0,
        'STATUS_CLASS_2XX'         = 1,
        'STATUS_CLASS_3XX'         = 2,
        'STATUS_CLASS_4XX'         = 3,
        'STATUS_CLASS_5XX'         = 4,
        'STATUS_CLASS_NO_STATUS'   = 5
    ),
    window_start    DateTime64(3),
    window_end      SimpleAggregateFunction(max, DateTime64(3)),
    count           SimpleAggregateFunction(sum, UInt64),
    sum_duration_ns SimpleAggregateFunction(sum, UInt64),
    req_bytes       SimpleAggregateFunction(sum, UInt64),
    resp_bytes      SimpleAggregateFunction(sum, UInt64),
    latency_sketch  SimpleAggregateFunction(sumMap, Map(Int32, UInt64)),
    status_codes    SimpleAggregateFunction(sumMap, Map(UInt32, UInt64)),
    deploy_version      LowCardinality(String),
    deploy_id           LowCardinality(String),
    environment         LowCardinality(String),
    region              LowCardinality(String),
    instance_start_time SimpleAggregateFunction(max, DateTime64(3)),
    max_duration_ns     SimpleAggregateFunction(max, UInt64),
    exemplars           SimpleAggregateFunction(groupArrayArray, Array(Tuple(DateTime64(3), UInt64, UInt32, String, String, String))),
    error_classes       SimpleAggregateFunction(sumMap, Map(String, UInt64)),
    no_status_reasons   SimpleAggregateFunction(sumMap, Map(UInt8, UInt64)),
    sum_downstream_duration_ns SimpleAggregateFunction(sum, UInt64)
)
ENGINE = AggregatingMergeTree
PARTITION BY toYYYYMMDD(window_start)
ORDER BY (tenant, service, route_template, status_class, method, instance, deploy_version, window_start);

-- Backfill every post-original column onto a pre-existing summaries table (the
-- CREATE above already carries them on a fresh install, so these are no-ops then).
-- deploy_version is intentionally NOT here: it is part of the sort key, which
-- ALTER cannot change (see the upgrade note above). Ordered before 0002's MVs.
ALTER TABLE summaries ADD COLUMN IF NOT EXISTS deploy_id           LowCardinality(String);
ALTER TABLE summaries ADD COLUMN IF NOT EXISTS environment         LowCardinality(String);
ALTER TABLE summaries ADD COLUMN IF NOT EXISTS region              LowCardinality(String);
ALTER TABLE summaries ADD COLUMN IF NOT EXISTS instance_start_time SimpleAggregateFunction(max, DateTime64(3));
ALTER TABLE summaries ADD COLUMN IF NOT EXISTS max_duration_ns     SimpleAggregateFunction(max, UInt64);
ALTER TABLE summaries ADD COLUMN IF NOT EXISTS exemplars           SimpleAggregateFunction(groupArrayArray, Array(Tuple(DateTime64(3), UInt64, UInt32, String, String, String)));
ALTER TABLE summaries ADD COLUMN IF NOT EXISTS error_classes       SimpleAggregateFunction(sumMap, Map(String, UInt64));
ALTER TABLE summaries ADD COLUMN IF NOT EXISTS no_status_reasons   SimpleAggregateFunction(sumMap, Map(UInt8, UInt64));
ALTER TABLE summaries ADD COLUMN IF NOT EXISTS sum_downstream_duration_ns SimpleAggregateFunction(sum, UInt64);

-- In-place type upgrade for instances whose summaries predates the
-- groupArrayArray exemplars fix: as a plain Array column on this
-- AggregatingMergeTree, same-key collapse kept one arbitrary row's array and
-- dropped the rest. SimpleAggregateFunction shares the underlying type's storage
-- layout, so this MODIFY is a metadata-only change and re-applying it on every
-- boot is a no-op — same idempotence class as the ADD COLUMN backfills above.
ALTER TABLE summaries MODIFY COLUMN exemplars SimpleAggregateFunction(groupArrayArray, Array(Tuple(DateTime64(3), UInt64, UInt32, String, String, String)));
