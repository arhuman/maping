# Benchmarks

The numbers below are the current output of the repository's own Go
benchmarks (`client/bench_test.go` and
`server/internal/ingest/bench_test.go`), run on a single developer machine
(Apple M3 Pro, `go1.26.5`, `darwin/arm64`). They characterize relative cost
and allocation behaviour, not a guaranteed production figure: absolute
numbers depend on hardware, Go version, and load. Reproduce them on your own
target hardware before relying on them for capacity planning.

## What is measured

- **`BenchmarkObserve` / `BenchmarkObserveParallel`** (`client/bench_test.go`):
  the cost of `Recorder.Observe` on the steady-state path, where the series
  already exists, so only shard-select, lock, counter increments, and
  `sketch.Add` run. This is the cost added to every recorded request.
- **`BenchmarkUploadSingleSummary` / `BenchmarkUploadBatch` /
  `BenchmarkUploadParallel`** (`server/internal/ingest/bench_test.go`): the
  cost of the ingest handler's `Upload` RPC, from authentication through
  timestamp policy, cardinality/rate checks, and row conversion, for a batch
  of 1 and a batch of 64 summaries.

## Measured results

Client, per-request hot path:

| Benchmark | Time/op | Allocations |
|---|---|---|
| `BenchmarkObserve` | 116.1 ns | 0 B/op, 0 allocs/op |
| `BenchmarkObserveParallel` | 94.97 ns | 0 B/op, 0 allocs/op |

The steady-state path is allocation-free, matching the design goal described
in [Runtime overhead](/doc/runtime-overhead): only the first observation of
a new series in a window allocates.

Server, ingest `Upload` RPC (per call, not per summary):

| Benchmark | Time/op | Allocations |
|---|---|---|
| `BenchmarkUploadSingleSummary` (1 summary) | 657.0 ns | 184 B/op, 4 allocs/op |
| `BenchmarkUploadBatch` (64 summaries) | 16342 ns | 5224 B/op, 130 allocs/op |
| `BenchmarkUploadParallel` (64 summaries, concurrent) | 5712 ns | 5224 B/op, 130 allocs/op |

A client flushes once per flush window (10 seconds by default) regardless of
request volume, so this per-upload cost is incurred once per window per
instance, not once per request.

## Reproducing these numbers

```bash
cd client && go test -run '^$' -bench 'BenchmarkObserve$|BenchmarkObserveParallel$' -benchmem ./...
cd server && go test -run '^$' -bench 'BenchmarkUpload' -benchmem ./internal/ingest/...
```
