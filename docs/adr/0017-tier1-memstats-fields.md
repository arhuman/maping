---
status: accepted
---

# Additive MemStats fields on the instance-window stream

The per-instance USE gauges of ADR-0015 (`cpu_ns`, `rss_bytes`, `heap_alloc_bytes`,
`gc_pause_ns`, `goroutines`) answer "is this replica saturated?" but leave the GC and
allocation story thin: STW pause time alone cannot distinguish "GC runs often but
cheaply" from "GC is eating concurrent CPU", and there is no allocation-rate signal at
all. The client already calls `runtime.ReadMemStats` once per flush window, so several
more fields from the same struct are free to collect.

## Decision

Extend `InstanceWindow` with six fields already in hand at sample time, mirroring the
existing gauges at every layer (proto, sampler, ingest, ClickHouse, aggregation query,
detail view). This extends ADR-0015; the stream, table, and separation rationale are
unchanged.

| Field | Type | Kind | Window aggregate |
|-------|------|------|------------------|
| `num_gc` | uint64 | counter delta | `sum` |
| `total_alloc_bytes` | uint64 | counter delta | `sum` |
| `mallocs` | uint64 | counter delta | `sum` |
| `gc_cpu_fraction` | double | gauge (0..1) | `avg` |
| `heap_inuse_bytes` | uint64 | gauge | `max` |
| `gomaxprocs` | uint32 | gauge | `max` |

- The three counter deltas are computed exactly like `cpu_ns`/`gc_pause_ns`: the sampler
  holds the previous cumulative value and reports `cur - prev` only once primed (zero on
  the priming call). The three gauges are point-in-time reads, no previous value held.
- The detail Resources panel gains, per instance and from these aggregates plus the
  window length: GC frequency (`num_gc`/window), GC CPU% (`gc_cpu_fraction`), allocation
  rate (`total_alloc_bytes`/window), and average allocation size
  (`total_alloc_bytes`/`mallocs`, guarded against zero).

## Backward compatibility

The change is additive and `buf breaking`-clean. The client SDK is a public, MIT-licensed
contract (ADR-0004), so wire evolution stays backward-compatible: old clients omit the new
fields (proto3 zero values), old servers ignore them.

## Migration

The six columns are declared in `0003`'s `CREATE TABLE` (fresh DBs get them directly) and
added to an existing table by `0004_instance_windows_memstats.sql` via idempotent
`ALTER TABLE ... ADD COLUMN IF NOT EXISTS`. On a fresh DB the ALTERs are no-ops; on an
existing dev DB they add the columns in place — non-destructive, no reset needed.

## Consequences

- The saturation verdicts become concrete: "GC frequency 8.3/s, GC CPU 18%, alloc rate
  5.1x baseline" instead of just a pause-time percentage.
- `gomaxprocs` lets CPU intensity be read as a share of available cores.
- Post-GC heap baseline and true OS RSS remain out of scope; they need a second runtime
  API and are deferred.
