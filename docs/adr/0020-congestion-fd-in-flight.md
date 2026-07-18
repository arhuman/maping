---
status: accepted
---

# File-descriptor and in-flight congestion gauges on the instance-window stream

The memory and CPU gauges of ADR-0015/0017/0018 make "busy" legible (CPU up, GC up,
heap rising), but they cannot see "blocked, not busy": latency climbs while CPU and GC
stay flat because the process is backing up on connections, files, or downstream waits.
Nothing on the `InstanceWindow` stream shows descriptor pressure or request concurrency,
so a saturation that is congestion rather than compute reads as a mystery.

## Decision

Extend `InstanceWindow` with three additive gauges, mirroring the existing gauges at every
layer (proto, sampler, ingest, ClickHouse, aggregation query, detail view). This extends
ADR-0018; the stream, table, and separation rationale are unchanged.

| Field | Type | Kind | Window aggregate | Source |
|-------|------|------|------------------|--------|
| `open_fds` | uint64 | gauge | `max` | Linux `/proc/self/fd` entry count |
| `fd_limit` | uint64 | gauge | `max` | Linux `syscall.Getrlimit(RLIMIT_NOFILE)` soft limit |
| `in_flight` | uint64 | gauge | `max` | recorder concurrency counter, window peak |

- `open_fds` and `fd_limit` are point-in-time OS reads on the already-paid per-flush
  sampling path (build-tagged, like `rss_true_bytes`): both 0 on non-Linux hosts, where
  the view renders an em-dash rather than a misleading `0 / 0`. Together they make
  nearness to the descriptor ceiling computable, and a rising `open_fds` proxies leaking
  or accumulating connections/files.
- `in_flight` is NOT an OS read. The recorder counts requests currently in flight and
  reports the window PEAK. Each adapter (`gin`, `echo`, `chi`, `nethttp`, `beego`) calls
  `defer rec.BeginRequest()()` at handler entry, incrementing a concurrency counter and
  raising a high-water mark via a lock-free CAS loop; the sampler takes-and-resets the
  peak once per window.

### Why the peak is taken-and-reset to the live in-flight count

`takeInFlightPeak` swaps the peak to the CURRENT in-flight count, not to zero. A window
whose requests are all still running would otherwise report a false dip on the next take.
Retaining the live level means the peak reflects the true high-water since the last window
even across long-running requests. The counter is safe and cheap on a no-op recorder: it
just counts harmlessly with no goroutine and no lock.

## Scope: first cut, zero app-wiring

This slice only collects, stores, and displays the three raw signals. It does not diagnose
(no congestion verdict, no near-the-ceiling rule) — that is a follow-up. It also requires
no host code changes: the FD gauges are automatic OS reads, and in-flight is wired through
the framework adapters the host already installs, so activation stays a matter of the
ingest key.

Database-pool saturation (`sql.DBStats`: open/in-use/wait count and wait duration) is the
natural next congestion signal but is deferred: unlike FDs and in-flight, it needs the host
to hand the SDK its `*sql.DB` behind an opt-in wiring, which this zero-wiring cut avoids.

## Backward compatibility

The change is additive and `buf breaking`-clean. The client SDK is a public, MIT-licensed
contract (ADR-0004), so wire evolution stays backward-compatible: old clients omit the new
fields (proto3 zero values), old servers ignore them.

## Migration

All three columns are declared in `0003`'s `CREATE TABLE` (fresh DBs get them directly) and
added to an existing table by `0006_instance_windows_congestion.sql` via idempotent
`ALTER TABLE ... ADD COLUMN IF NOT EXISTS`. On a fresh DB the ALTERs are no-ops; on an
existing dev DB they add the columns in place — non-destructive, no reset needed.

## Consequences

- The detail Resources panel gains an FD column (`open_fds / fd_limit`, em-dash when
  unavailable) and an IN-FLIGHT column (peak concurrency), the first signals on the stream
  that separate a blocked process from a busy one.
- A congestion verdict, a descriptor-headroom threshold, and DB-pool saturation remain out
  of scope; this slice only collects, stores, and displays the raw numbers.
