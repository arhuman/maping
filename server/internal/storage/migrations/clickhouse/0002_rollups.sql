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
    window_end      DateTime64(3),
    count           UInt64,
    sum_duration_ns UInt64,
    req_bytes       UInt64,
    resp_bytes      UInt64,
    latency_sketch  Map(Int32, UInt64),
    status_codes    Map(UInt32, UInt64)
)
ENGINE = AggregatingMergeTree
PARTITION BY toYYYYMMDD(window_start)
ORDER BY (tenant, service, route_template, status_class, window_start);

CREATE MATERIALIZED VIEW IF NOT EXISTS summaries_1m_mv TO summaries_1m AS
SELECT
    tenant, service, instance, method, route_template, status_class,
    toStartOfInterval(window_start, INTERVAL 1 minute) AS window_start,
    toStartOfInterval(window_end, INTERVAL 1 minute)   AS window_end,
    sum(count)                     AS count,
    sum(sum_duration_ns)           AS sum_duration_ns,
    sum(req_bytes)                 AS req_bytes,
    sum(resp_bytes)                AS resp_bytes,
    sumMap(latency_sketch)         AS latency_sketch,
    sumMap(status_codes)           AS status_codes
FROM summaries
GROUP BY tenant, service, instance, method, route_template, status_class, window_start, window_end;

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
    window_end      DateTime64(3),
    count           UInt64,
    sum_duration_ns UInt64,
    req_bytes       UInt64,
    resp_bytes      UInt64,
    latency_sketch  Map(Int32, UInt64),
    status_codes    Map(UInt32, UInt64)
)
ENGINE = AggregatingMergeTree
PARTITION BY toYYYYMMDD(window_start)
ORDER BY (tenant, service, route_template, status_class, window_start);

CREATE MATERIALIZED VIEW IF NOT EXISTS summaries_1h_mv TO summaries_1h AS
SELECT
    tenant, service, instance, method, route_template, status_class,
    toStartOfInterval(window_start, INTERVAL 1 hour) AS window_start,
    toStartOfInterval(window_end, INTERVAL 1 hour)   AS window_end,
    sum(count)                     AS count,
    sum(sum_duration_ns)           AS sum_duration_ns,
    sum(req_bytes)                 AS req_bytes,
    sum(resp_bytes)                AS resp_bytes,
    sumMap(latency_sketch)         AS latency_sketch,
    sumMap(status_codes)           AS status_codes
FROM summaries_1m
GROUP BY tenant, service, instance, method, route_template, status_class, window_start, window_end;

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
    window_end      DateTime64(3),
    count           UInt64,
    sum_duration_ns UInt64,
    req_bytes       UInt64,
    resp_bytes      UInt64,
    latency_sketch  Map(Int32, UInt64),
    status_codes    Map(UInt32, UInt64)
)
ENGINE = AggregatingMergeTree
PARTITION BY toYYYYMMDD(window_start)
ORDER BY (tenant, service, route_template, status_class, window_start);

CREATE MATERIALIZED VIEW IF NOT EXISTS summaries_1d_mv TO summaries_1d AS
SELECT
    tenant, service, instance, method, route_template, status_class,
    toStartOfInterval(window_start, INTERVAL 1 day) AS window_start,
    toStartOfInterval(window_end, INTERVAL 1 day)   AS window_end,
    sum(count)                     AS count,
    sum(sum_duration_ns)           AS sum_duration_ns,
    sum(req_bytes)                 AS req_bytes,
    sum(resp_bytes)                AS resp_bytes,
    sumMap(latency_sketch)         AS latency_sketch,
    sumMap(status_codes)           AS status_codes
FROM summaries_1h
GROUP BY tenant, service, instance, method, route_template, status_class, window_start, window_end;

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
