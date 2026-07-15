-- 0003_instance_windows.sql — per-instance USE (saturation) gauges.
--
-- A separate stream from the per-endpoint summaries: one row is one process's
-- resource snapshot (CPU, memory, goroutines, GC pause) over one flush window,
-- sampled by the client's runtime sampler and stamped with the server-resolved
-- tenant. It answers "did p99 rise because GC pauses tripled / goroutines blew
-- up?" with no release, by putting saturation next to the RED metrics.
--
-- This is a NEW table (not an alter of summaries), so it needs no one-time drop:
-- a plain MergeTree keyed per instance over time. cpu_ns and gc_pause_ns are
-- per-window deltas; rss/heap/goroutines are point-in-time gauges. No rollup tiers
-- for now (the plan defers 1m/1h aggregation); the raw stream carries a short TTL
-- matching the summaries raw tier.

CREATE TABLE IF NOT EXISTS instance_windows
(
    tenant           String,
    service          String,
    instance         String,
    window_start     DateTime64(3),
    window_end       DateTime64(3),
    cpu_ns           UInt64,
    rss_bytes        UInt64,
    heap_alloc_bytes UInt64,
    gc_pause_ns      UInt64,
    goroutines       UInt64
)
ENGINE = MergeTree
PARTITION BY toYYYYMMDD(window_start)
ORDER BY (tenant, service, instance, window_start)
TTL toDateTime(window_start) + INTERVAL 7 DAY;
