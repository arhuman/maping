# Runtime overhead

mAPI-ng is designed to sit on every request of a hot path, so its cost has
to be small and bounded regardless of traffic volume or the diversity of
routes a service exposes.

## In-process aggregation, not per-request shipping

Every request is folded into an in-memory aggregate (a DDSketch plus a
handful of counters) keyed by `(method, route-template, status-class)`. A
busy service sends one small Summary per series per flush window instead of
one network call per request. The aggregation map is sharded (16 shards,
selected by a hash of the series key) so concurrent requests on different
series rarely contend on the same lock.

On the steady-state path, an existing series is looked up and its counters
and sketch are updated in place: this path performs no heap allocations.
Only the first request of a new series in a window allocates (a map entry
and a new sketch). See [Benchmarks](/doc/benchmarks) for measured numbers.

## The flush window bounds network and CPU cost

The client accumulates in process for a flush window (10 seconds by
default, configurable) before sending anything. Sending happens on a
background goroutine, off the request path: `Observe` never performs
network I/O, so a slow or unreachable collector cannot add latency to a
request.

## Cardinality is bounded by construction

The series key uses route templates, never raw paths, so a request to
`/users/12345` and one to `/users/67890` fold into the same series
`GET /users/:id`. This keeps the number of distinct series - and therefore
memory and CPU for aggregation - proportional to the number of routes a
service registers, not to the number of requests or distinct path values it
receives. See [What data is collected](/doc/data-collected) for the exact
fields aggregated.

## No per-request storage

Because only the aggregate is kept, the client's memory footprint is bounded
by `(number of active series) x (sketch size)`, not by request volume. The
sketch itself is bounded: latency is clamped to `[1us, 60s]`, which caps the
number of buckets it can hold.

For measured client and server request-path cost, see
[Benchmarks](/doc/benchmarks).
