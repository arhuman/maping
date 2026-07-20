# What data is collected

mAPI-ng ships **Summaries**, not per-request events. Each flush window (10
seconds by default), the client aggregates every completed request in
process into one Summary per series, where a series is
`(method, route-template, status-class)`. Only the aggregate crosses the
wire.

## Fields in a Summary

| Field | Meaning |
|---|---|
| `count` | Number of requests in this window for this series (drives rate) |
| `latency sketch` (DDSketch) | Mergeable latency distribution, queried as p50/p95/p99 |
| `sum_duration` | Sum of request durations, for an exact mean |
| `status_code_breakdown` | Exact status codes observed, bounded to the 20 most frequent per series |
| `error_class_breakdown` | Bucketed error causes (panic, write-timeout, etc.) |
| `request/response byte sums` | Total bytes in and out, for bandwidth per endpoint |
| `sum_downstream_duration` | Time spent in tracked downstream calls, where instrumented |

Every upload also carries an envelope with `service`, `instance`,
`sdk_version`, and `dropped_summaries` (client-side backpressure loss, made
visible rather than silently swallowed). Error rate is derived from
status-class counts at query time; it is not stored as a separate field.

## What is not collected

- **No per-request events.** Individual requests are aggregated in process
  and discarded; only the window's Summary leaves the service. Per-request
  forensics are out of scope.
- **No caller identity.** Summaries carry no client IP, user ID, or session
  information.
- **No custom tags or user-defined labels.** The series key is closed and
  fully auto-derived; there is no mechanism to attach arbitrary labels in v1.
- **Route templates only, never raw paths or query strings.** An endpoint is
  always the registered route template (e.g. `GET /users/:id`), derived from
  the framework's routing (`c.FullPath()` on Gin). mAPI-ng never emits a raw
  request path, so a per-value path (`/users/12345`) or a query string cannot
  inflate the number of distinct series a service reports.
- **No in-flight/concurrency gauges beyond the per-instance USE stream.**
  Saturation (CPU, RSS, goroutines, GC pauses) is sampled once per flush
  window per instance and shipped as a separate, low-cardinality stream, not
  folded into the endpoint series.

See [Security & data flow](/doc/security-data-flow) for how a Summary moves
from the client to storage, and [Runtime overhead](/doc/runtime-overhead) for
why this shape keeps the client cheap.
