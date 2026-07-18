---
status: accepted
---

# Post-GC heap baseline and true RSS on the instance-window stream

The memory gauges of ADR-0015/0017 (`rss_bytes`, `heap_alloc_bytes`, `heap_inuse_bytes`)
cannot tell a leak from a burst. `heap_alloc_bytes` and `heap_inuse_bytes` are arbitrary
sample points: a slow leak staircase and a transient allocation spike look identical on
them. And `rss_bytes` is actually `runtime.MemStats.Sys` — reserved address space, not the
process's real resident footprint — so it overstates memory and never falls when pages are
returned to the OS.

## Decision

Extend `InstanceWindow` with two additive point-in-time gauges, mirroring the existing
gauges at every layer (proto, sampler, ingest, ClickHouse, aggregation query, detail view).
This extends ADR-0015/0017; the stream, table, and separation rationale are unchanged.

| Field | Type | Kind | Window aggregate | Source |
|-------|------|------|------------------|--------|
| `post_gc_heap_bytes` | uint64 | gauge | `max` | `runtime/metrics` `/gc/heap/live:bytes` |
| `rss_true_bytes` | uint64 | gauge | `max` | Linux `/proc/self/statm` (resident pages x pagesize) |

- `post_gc_heap_bytes` is the live heap as of the last completed GC mark. Because it is
  measured at the same point in every GC cycle, a monotonically rising value is a leak,
  whereas a spike that falls back is a burst — the distinction `heap_alloc_bytes` cannot make.
- `rss_true_bytes` is a NEW field, not a redefinition: `rss_bytes` keeps its `Sys` meaning so
  existing readers are undisturbed. It is best-effort — 0 on non-Linux hosts, where the view
  renders an em-dash rather than a misleading "0 B".

### Why `/gc/heap/live:bytes` over `min(HeapAlloc)`

The post-GC baseline could be approximated by tracking the minimum `HeapAlloc` across a
window, but that needs a background ticker sampling `ReadMemStats` between flushes to catch a
post-GC trough. `runtime/metrics` exposes the exact post-mark live heap directly, so it is one
extra `metrics.Read` on the already-paid per-flush sampling path — no ticker, no goroutine, no
guessing. The read reuses a single `[]metrics.Sample` buffer and guards against an unsupported
metric (`KindBad`/non-uint64) by reporting 0.

## Backward compatibility

The change is additive and `buf breaking`-clean. The client SDK is a public, MIT-licensed
contract (ADR-0004), so wire evolution stays backward-compatible: old clients omit the new
fields (proto3 zero values), old servers ignore them.

## Migration

Both columns are declared in `0003`'s `CREATE TABLE` (fresh DBs get them directly) and added
to an existing table by `0005_instance_windows_post_gc_heap.sql` via idempotent
`ALTER TABLE ... ADD COLUMN IF NOT EXISTS`. On a fresh DB the ALTERs are no-ops; on an existing
dev DB they add the columns in place — non-destructive, no reset needed.

## Consequences

- The detail Resources panel shows the post-GC heap baseline and true RSS per instance, the
  first two numbers on the stream that can separate a leak from an allocation burst.
- The leak-vs-burst verdict, a memory-trend time series, and any series-over-time query for
  instance windows remain out of scope; this slice only collects, stores, and displays the two
  raw numbers.
