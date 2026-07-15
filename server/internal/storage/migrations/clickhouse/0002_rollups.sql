-- 0002_rollups.sql — cascading rollup tiers and retention TTL for the data plane.
--
-- Tiers: 10s (raw summaries) -> 1m -> 1h -> 1d. Each coarser tier is a
-- TARGET table fed by a MATERIALIZED VIEW reading the tier below it, summing
-- counters and merging the DDSketch bucket-wise with sumMap (mergeability is
-- what makes rollups exact — ADR-0003). The query layer still GROUP BYs and
-- sum/sumMaps at read time, so correctness does not depend on the engine fully
-- collapsing partial-aggregate rows; the coarser time bucketing is the disk win.
--
-- The frozen percentile SQL (query.go) is unchanged: it only parameterizes the
-- FROM table across these tiers.
--
-- All statements are idempotent (IF NOT EXISTS) so the migration re-applies
-- cleanly. TTLs mirror plan retention: the raw fine tier is dropped after it has
-- been rolled up (ADR-0003), coarser tiers live progressively longer.
--
-- UPGRADE PATH — tables AND materialized views are updated in place. Each rollup
-- TABLE gets new columns via ALTER ... ADD COLUMN IF NOT EXISTS below, and each MV
-- gets its projection re-applied via ALTER TABLE ..._mv MODIFY QUERY right after its
-- CREATE ... IF NOT EXISTS. CREATE IF NOT EXISTS alone would SKIP an already-existing
-- MV (leaving a pre-existing instance's rollups stuck on the old projection); the
-- MODIFY QUERY updates the view in place — no drop, so no propagation gap and no
-- lost rollup rows — so a newly-added column starts rolling up on the next flush.
-- MODIFY QUERY does not backfill: rows already rolled up keep the column default,
-- while new windows carry the added column. On a FRESH install the CREATE builds the
-- correct MV and the MODIFY QUERY is a redundant no-op.
--
-- Because the projection is written twice per MV (CREATE + MODIFY QUERY), the two
-- copies MUST stay byte-identical; a unit test (migrate_test.go) enforces this.

-- ---------------------------------------------------------------------------
-- 1-minute tier
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS summaries_1m
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
    error_classes       SimpleAggregateFunction(sumMap, Map(String, UInt64)),
    no_status_reasons   SimpleAggregateFunction(sumMap, Map(UInt8, UInt64)),
    sum_downstream_duration_ns SimpleAggregateFunction(sum, UInt64)
)
ENGINE = AggregatingMergeTree
PARTITION BY toYYYYMMDD(window_start)
ORDER BY (tenant, service, route_template, status_class, method, instance, deploy_version, window_start);

-- Backfill post-original columns onto a pre-existing summaries_1m before the MV
-- below selects them (deploy_version stays out — sort key; exemplars are raw-tier
-- only). No-ops on a fresh install. See the upgrade note in 0001.
ALTER TABLE summaries_1m ADD COLUMN IF NOT EXISTS deploy_id           LowCardinality(String);
ALTER TABLE summaries_1m ADD COLUMN IF NOT EXISTS environment         LowCardinality(String);
ALTER TABLE summaries_1m ADD COLUMN IF NOT EXISTS region              LowCardinality(String);
ALTER TABLE summaries_1m ADD COLUMN IF NOT EXISTS instance_start_time SimpleAggregateFunction(max, DateTime64(3));
ALTER TABLE summaries_1m ADD COLUMN IF NOT EXISTS max_duration_ns     SimpleAggregateFunction(max, UInt64);
ALTER TABLE summaries_1m ADD COLUMN IF NOT EXISTS error_classes       SimpleAggregateFunction(sumMap, Map(String, UInt64));
ALTER TABLE summaries_1m ADD COLUMN IF NOT EXISTS no_status_reasons   SimpleAggregateFunction(sumMap, Map(UInt8, UInt64));
ALTER TABLE summaries_1m ADD COLUMN IF NOT EXISTS sum_downstream_duration_ns SimpleAggregateFunction(sum, UInt64);

CREATE MATERIALIZED VIEW IF NOT EXISTS summaries_1m_mv TO summaries_1m AS
SELECT
    tenant, service, instance, method, route_template, status_class,
    deploy_version, deploy_id, environment, region,
    toStartOfInterval(window_start, INTERVAL 1 minute) AS window_start,
    max(toStartOfInterval(window_end, INTERVAL 1 minute)) AS window_end,
    sum(count)                     AS count,
    sum(sum_duration_ns)           AS sum_duration_ns,
    sum(req_bytes)                 AS req_bytes,
    sum(resp_bytes)                AS resp_bytes,
    sumMap(latency_sketch)         AS latency_sketch,
    sumMap(status_codes)           AS status_codes,
    max(instance_start_time)       AS instance_start_time,
    max(max_duration_ns)           AS max_duration_ns,
    sumMap(error_classes)          AS error_classes,
    sumMap(no_status_reasons)      AS no_status_reasons,
    sum(sum_downstream_duration_ns) AS sum_downstream_duration_ns
FROM summaries
GROUP BY tenant, service, instance, method, route_template, status_class,
         deploy_version, deploy_id, environment, region, window_start;

-- Re-apply the SAME projection in place so a PRE-EXISTING summaries_1m_mv (which
-- CREATE IF NOT EXISTS above leaves untouched) starts rolling up the columns added
-- since it was first created. MODIFY QUERY updates the view without dropping it, so
-- there is no propagation gap; it does not backfill, so rows already rolled up keep
-- the column default. KEEP THIS SELECT BYTE-IDENTICAL TO THE CREATE ABOVE.
ALTER TABLE summaries_1m_mv MODIFY QUERY
SELECT
    tenant, service, instance, method, route_template, status_class,
    deploy_version, deploy_id, environment, region,
    toStartOfInterval(window_start, INTERVAL 1 minute) AS window_start,
    max(toStartOfInterval(window_end, INTERVAL 1 minute)) AS window_end,
    sum(count)                     AS count,
    sum(sum_duration_ns)           AS sum_duration_ns,
    sum(req_bytes)                 AS req_bytes,
    sum(resp_bytes)                AS resp_bytes,
    sumMap(latency_sketch)         AS latency_sketch,
    sumMap(status_codes)           AS status_codes,
    max(instance_start_time)       AS instance_start_time,
    max(max_duration_ns)           AS max_duration_ns,
    sumMap(error_classes)          AS error_classes,
    sumMap(no_status_reasons)      AS no_status_reasons,
    sum(sum_downstream_duration_ns) AS sum_downstream_duration_ns
FROM summaries
GROUP BY tenant, service, instance, method, route_template, status_class,
         deploy_version, deploy_id, environment, region, window_start;

-- ---------------------------------------------------------------------------
-- 1-hour tier (fed from 1m)
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS summaries_1h
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
    error_classes       SimpleAggregateFunction(sumMap, Map(String, UInt64)),
    no_status_reasons   SimpleAggregateFunction(sumMap, Map(UInt8, UInt64)),
    sum_downstream_duration_ns SimpleAggregateFunction(sum, UInt64)
)
ENGINE = AggregatingMergeTree
PARTITION BY toYYYYMMDD(window_start)
ORDER BY (tenant, service, route_template, status_class, method, instance, deploy_version, window_start);

-- Backfill post-original columns onto summaries_1h before the MV below (which
-- reads FROM summaries_1m) selects them. No-ops on a fresh install.
ALTER TABLE summaries_1h ADD COLUMN IF NOT EXISTS deploy_id           LowCardinality(String);
ALTER TABLE summaries_1h ADD COLUMN IF NOT EXISTS environment         LowCardinality(String);
ALTER TABLE summaries_1h ADD COLUMN IF NOT EXISTS region              LowCardinality(String);
ALTER TABLE summaries_1h ADD COLUMN IF NOT EXISTS instance_start_time SimpleAggregateFunction(max, DateTime64(3));
ALTER TABLE summaries_1h ADD COLUMN IF NOT EXISTS max_duration_ns     SimpleAggregateFunction(max, UInt64);
ALTER TABLE summaries_1h ADD COLUMN IF NOT EXISTS error_classes       SimpleAggregateFunction(sumMap, Map(String, UInt64));
ALTER TABLE summaries_1h ADD COLUMN IF NOT EXISTS no_status_reasons   SimpleAggregateFunction(sumMap, Map(UInt8, UInt64));
ALTER TABLE summaries_1h ADD COLUMN IF NOT EXISTS sum_downstream_duration_ns SimpleAggregateFunction(sum, UInt64);

CREATE MATERIALIZED VIEW IF NOT EXISTS summaries_1h_mv TO summaries_1h AS
SELECT
    tenant, service, instance, method, route_template, status_class,
    deploy_version, deploy_id, environment, region,
    toStartOfInterval(window_start, INTERVAL 1 hour) AS window_start,
    max(toStartOfInterval(window_end, INTERVAL 1 hour)) AS window_end,
    sum(count)                     AS count,
    sum(sum_duration_ns)           AS sum_duration_ns,
    sum(req_bytes)                 AS req_bytes,
    sum(resp_bytes)                AS resp_bytes,
    sumMap(latency_sketch)         AS latency_sketch,
    sumMap(status_codes)           AS status_codes,
    max(instance_start_time)       AS instance_start_time,
    max(max_duration_ns)           AS max_duration_ns,
    sumMap(error_classes)          AS error_classes,
    sumMap(no_status_reasons)      AS no_status_reasons,
    sum(sum_downstream_duration_ns) AS sum_downstream_duration_ns
FROM summaries_1m
GROUP BY tenant, service, instance, method, route_template, status_class,
         deploy_version, deploy_id, environment, region, window_start;

-- In-place projection update for a pre-existing summaries_1h_mv (see the 1m note).
-- KEEP THIS SELECT BYTE-IDENTICAL TO THE CREATE ABOVE.
ALTER TABLE summaries_1h_mv MODIFY QUERY
SELECT
    tenant, service, instance, method, route_template, status_class,
    deploy_version, deploy_id, environment, region,
    toStartOfInterval(window_start, INTERVAL 1 hour) AS window_start,
    max(toStartOfInterval(window_end, INTERVAL 1 hour)) AS window_end,
    sum(count)                     AS count,
    sum(sum_duration_ns)           AS sum_duration_ns,
    sum(req_bytes)                 AS req_bytes,
    sum(resp_bytes)                AS resp_bytes,
    sumMap(latency_sketch)         AS latency_sketch,
    sumMap(status_codes)           AS status_codes,
    max(instance_start_time)       AS instance_start_time,
    max(max_duration_ns)           AS max_duration_ns,
    sumMap(error_classes)          AS error_classes,
    sumMap(no_status_reasons)      AS no_status_reasons,
    sum(sum_downstream_duration_ns) AS sum_downstream_duration_ns
FROM summaries_1m
GROUP BY tenant, service, instance, method, route_template, status_class,
         deploy_version, deploy_id, environment, region, window_start;

-- ---------------------------------------------------------------------------
-- 1-day tier (fed from 1h)
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS summaries_1d
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
    error_classes       SimpleAggregateFunction(sumMap, Map(String, UInt64)),
    no_status_reasons   SimpleAggregateFunction(sumMap, Map(UInt8, UInt64)),
    sum_downstream_duration_ns SimpleAggregateFunction(sum, UInt64)
)
ENGINE = AggregatingMergeTree
PARTITION BY toYYYYMMDD(window_start)
ORDER BY (tenant, service, route_template, status_class, method, instance, deploy_version, window_start);

-- Backfill post-original columns onto summaries_1d before the MV below (which
-- reads FROM summaries_1h) selects them. No-ops on a fresh install.
ALTER TABLE summaries_1d ADD COLUMN IF NOT EXISTS deploy_id           LowCardinality(String);
ALTER TABLE summaries_1d ADD COLUMN IF NOT EXISTS environment         LowCardinality(String);
ALTER TABLE summaries_1d ADD COLUMN IF NOT EXISTS region              LowCardinality(String);
ALTER TABLE summaries_1d ADD COLUMN IF NOT EXISTS instance_start_time SimpleAggregateFunction(max, DateTime64(3));
ALTER TABLE summaries_1d ADD COLUMN IF NOT EXISTS max_duration_ns     SimpleAggregateFunction(max, UInt64);
ALTER TABLE summaries_1d ADD COLUMN IF NOT EXISTS error_classes       SimpleAggregateFunction(sumMap, Map(String, UInt64));
ALTER TABLE summaries_1d ADD COLUMN IF NOT EXISTS no_status_reasons   SimpleAggregateFunction(sumMap, Map(UInt8, UInt64));
ALTER TABLE summaries_1d ADD COLUMN IF NOT EXISTS sum_downstream_duration_ns SimpleAggregateFunction(sum, UInt64);

CREATE MATERIALIZED VIEW IF NOT EXISTS summaries_1d_mv TO summaries_1d AS
SELECT
    tenant, service, instance, method, route_template, status_class,
    deploy_version, deploy_id, environment, region,
    toStartOfInterval(window_start, INTERVAL 1 day) AS window_start,
    max(toStartOfInterval(window_end, INTERVAL 1 day)) AS window_end,
    sum(count)                     AS count,
    sum(sum_duration_ns)           AS sum_duration_ns,
    sum(req_bytes)                 AS req_bytes,
    sum(resp_bytes)                AS resp_bytes,
    sumMap(latency_sketch)         AS latency_sketch,
    sumMap(status_codes)           AS status_codes,
    max(instance_start_time)       AS instance_start_time,
    max(max_duration_ns)           AS max_duration_ns,
    sumMap(error_classes)          AS error_classes,
    sumMap(no_status_reasons)      AS no_status_reasons,
    sum(sum_downstream_duration_ns) AS sum_downstream_duration_ns
FROM summaries_1h
GROUP BY tenant, service, instance, method, route_template, status_class,
         deploy_version, deploy_id, environment, region, window_start;

-- In-place projection update for a pre-existing summaries_1d_mv (see the 1m note).
-- KEEP THIS SELECT BYTE-IDENTICAL TO THE CREATE ABOVE.
ALTER TABLE summaries_1d_mv MODIFY QUERY
SELECT
    tenant, service, instance, method, route_template, status_class,
    deploy_version, deploy_id, environment, region,
    toStartOfInterval(window_start, INTERVAL 1 day) AS window_start,
    max(toStartOfInterval(window_end, INTERVAL 1 day)) AS window_end,
    sum(count)                     AS count,
    sum(sum_duration_ns)           AS sum_duration_ns,
    sum(req_bytes)                 AS req_bytes,
    sum(resp_bytes)                AS resp_bytes,
    sumMap(latency_sketch)         AS latency_sketch,
    sumMap(status_codes)           AS status_codes,
    max(instance_start_time)       AS instance_start_time,
    max(max_duration_ns)           AS max_duration_ns,
    sumMap(error_classes)          AS error_classes,
    sumMap(no_status_reasons)      AS no_status_reasons,
    sum(sum_downstream_duration_ns) AS sum_downstream_duration_ns
FROM summaries_1h
GROUP BY tenant, service, instance, method, route_template, status_class,
         deploy_version, deploy_id, environment, region, window_start;

-- ---------------------------------------------------------------------------
-- Retention TTL. Each tier expires after it has been rolled into the next; the
-- coarser tiers live longer. Values mirror plan retention (ADR-0003). The raw
-- fine tier is dropped once its 1m rollup exists, saving disk.
--
-- window_start is DateTime64(3) but a TTL expression must evaluate to Date or
-- DateTime, so it is coerced with toDateTime() (day-granular expiry needs no
-- sub-second precision).
-- ---------------------------------------------------------------------------
ALTER TABLE summaries    MODIFY TTL toDateTime(window_start) + INTERVAL 7 DAY;
ALTER TABLE summaries_1m MODIFY TTL toDateTime(window_start) + INTERVAL 30 DAY;
ALTER TABLE summaries_1h MODIFY TTL toDateTime(window_start) + INTERVAL 180 DAY;
ALTER TABLE summaries_1d MODIFY TTL toDateTime(window_start) + INTERVAL 730 DAY;
