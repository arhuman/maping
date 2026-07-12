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
