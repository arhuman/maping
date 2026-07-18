-- 0003_instance_windows.sql — per-instance USE (saturation) gauges.
--
-- A separate stream from the per-endpoint summaries: one row is one process's
-- resource snapshot (CPU, memory, goroutines, GC pause) over one flush window,
-- sampled by the client's runtime sampler and stamped with the server-resolved
-- tenant. It answers "did p99 rise because GC pauses tripled / goroutines blew
-- up?" with no release, by putting saturation next to the RED metrics.
--
-- This is a NEW table (not an alter of summaries), so it needs no one-time drop:
-- a plain MergeTree keyed per instance over time. cpu_ns, gc_pause_ns, num_gc,
-- total_alloc_bytes and mallocs are per-window deltas; rss/heap/goroutines/
-- gc_cpu_fraction/heap_inuse_bytes/gomaxprocs are point-in-time gauges. No rollup
-- tiers for now; the raw stream carries a short TTL matching the summaries raw tier.
--
-- num_gc..gomaxprocs are additive MemStats fields already read per sample by the
-- client (near-zero cost). They are declared here so a fresh DB gets them directly;
-- 0004 ADDs the same columns to an existing dev DB (non-destructive, no reset).

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
    goroutines       UInt64,
    num_gc            UInt64,
    total_alloc_bytes UInt64,
    mallocs           UInt64,
    gc_cpu_fraction   Float64,
    heap_inuse_bytes  UInt64,
    gomaxprocs        UInt32
)
ENGINE = MergeTree
PARTITION BY toYYYYMMDD(window_start)
ORDER BY (tenant, service, instance, window_start)
TTL toDateTime(window_start) + INTERVAL 7 DAY;
