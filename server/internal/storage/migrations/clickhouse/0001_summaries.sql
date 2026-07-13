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
-- ApplyMigrations re-runs every .sql on startup and relies on CREATE ... IF NOT
-- EXISTS for idempotency, so it will NOT upgrade a table that already exists. A
-- pre-existing dev instance carrying the OLD schema must be reset ONCE by hand
-- (data is disposable pre-GA); this migration deliberately does NOT automate the
-- drop, so it can never wipe data on a routine restart:
--
--   DROP TABLE IF EXISTS summaries, summaries_1m, summaries_1h, summaries_1d;
--   DROP VIEW  IF EXISTS summaries_1m_mv, summaries_1h_mv, summaries_1d_mv;
--
-- then let the next startup recreate them from the edited 0001/0002.

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
    status_codes    SimpleAggregateFunction(sumMap, Map(UInt32, UInt64))
)
ENGINE = AggregatingMergeTree
PARTITION BY toYYYYMMDD(window_start)
ORDER BY (tenant, service, route_template, status_class, method, instance, window_start);
