---
status: accepted
---

# USE gauges: per-instance saturation as a separate stream

RED metrics and the error/downstream breakdowns explain *what* a request did and
*where* its time went, but not *why the machine itself* slowed down. A p99 rise
with no code change is often saturation — GC pauses tripling, goroutines blowing
up, memory pressure — which the per-endpoint summaries cannot show because they
are keyed by endpoint, not by process health.

## Decision

Ship **per-instance USE (Utilization/Saturation/Errors) gauges** as a **separate
stream** alongside the summaries, stored in their **own table**, not as new
columns on `summaries`.

- **`InstanceWindow`** (new proto message) on `UploadRequest` (field 3, repeated):
  `window_start/end_ms`, `cpu_ns`, `rss_bytes`, `heap_alloc_bytes`, `gc_pause_ns`,
  `goroutines`. One entry per upload, sampled once per flush window.
- **Client sampler** reads `runtime.ReadMemStats`, `runtime.NumGoroutine`, and
  process CPU time (`getrusage` on unix; 0 elsewhere via build tags). `cpu_ns` and
  `gc_pause_ns` are reported as the **per-window delta** of monotonic counters; the
  rest are point-in-time reads. The first sample primes the baselines and reports
  zero deltas.
- **`instance_windows`** table (migration `0003`): a plain `MergeTree` keyed
  `(tenant, service, instance, window_start)` with a short TTL. No rollup tiers for
  now — saturation is a recent-debugging signal.

## Why a separate table, not columns on `summaries`

USE gauges are **per process**, not per (endpoint, status-class). Folding them into
`summaries` would either duplicate them across every endpoint row of an instance
(wrong sums) or force an awkward endpoint dimension onto a process-level fact. A
dedicated table keyed per instance keeps the model honest and the summaries
schema — and its frozen cardinality/series keys — untouched.

## Ingest and writer

A new **optional** `InstanceWindowSink` (wired via `WithInstanceWindowSink`) keeps
the change additive: the zero-option handler ignores instance windows, so existing
tests and dev-without-the-sink are unaffected. The `storage.Writer` gains a second
buffered stream and batcher branch sharing the same drop-on-failure steady flush
and bounded-retry final drain as the summaries stream, so a bad ClickHouse never
wedges ingest. Windows pass the same timestamp-skew policy as summaries.

## Consequences

- "p99 rose because GC pauses tripled / goroutines blew up" is answerable per
  replica, with no release, from `InstanceResourcesForService` (CPU and GC-pause
  summed, memory/goroutines peaked) behind a Resources panel on the endpoint
  detail.
- `cpu_ns` is zero on non-unix platforms (best-effort, build-tagged); `rss_bytes`
  is the runtime `Sys` proxy, not true OS RSS.
- New table ⇒ **no one-time reset** needed (unlike the in-place `summaries` edits
  of ADR-0012/0013/0014); `0003` is a plain idempotent `CREATE TABLE IF NOT EXISTS`.
- Rollup tiers for `instance_windows` are deferred; the raw stream under its TTL is
  enough for the debugging use case.
