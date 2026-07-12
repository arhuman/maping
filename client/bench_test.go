package maping

import (
	"context"
	"testing"
	"time"

	mapingv1 "github.com/arhuman/maping/proto/maping/v1"
)

// benchRecords is a small, fixed set of steady-state records covering distinct
// series (method/route/class combinations). Each is observed at least once
// before the timed loop so the hot path only ever hits the "series exists"
// branch — the branch that must be allocation-free.
var benchRecords = []Record{
	{Method: "GET", RouteTemplate: "/users/:id", Status: 200, Duration: 12 * time.Millisecond, ReqBytes: 128, RespBytes: 512},
	{Method: "GET", RouteTemplate: "/users/:id", Status: 500, Duration: 40 * time.Millisecond},
	{Method: "POST", RouteTemplate: "/orders", Status: 201, Duration: 8 * time.Millisecond, ReqBytes: 256, RespBytes: 64},
	{Method: "GET", RouteTemplate: "/products", Status: 200, Duration: 3 * time.Millisecond},
	{Method: "DELETE", RouteTemplate: "/orders/:id", Status: 404, Duration: 1 * time.Millisecond},
	{Method: "PUT", RouteTemplate: "/users/:id", Status: 200, Duration: 20 * time.Millisecond},
	{Method: "GET", RouteTemplate: "/health", Status: 200, Duration: 200 * time.Microsecond},
	{Method: "POST", RouteTemplate: "/login", Status: 401, Duration: 5 * time.Millisecond},
}

// newBenchRecorder builds a recorder wired to a no-op transport, with every
// bench series pre-populated so Observe stays on the steady-state (no-alloc)
// path throughout the timed loop.
func newBenchRecorder() *Recorder {
	shards := new([numShards]shard)
	for i := range shards {
		shards[i].m = make(map[seriesKey]*series)
	}
	r := &Recorder{
		cfg:    Config{Service: "svc", Instance: "inst", FlushWindow: time.Second},
		tx:     noopUploader{},
		shards: shards,
	}
	for _, rec := range benchRecords {
		r.Observe(rec) // first-sight allocation happens here, outside timing
	}
	return r
}

// noopUploader is a zero-cost Uploader for benchmarks (never called on the hot
// path, only satisfies active()).
type noopUploader struct{}

func (noopUploader) Upload(context.Context, *mapingv1.UploadRequest) error { return nil }
func (noopUploader) Register(context.Context, *mapingv1.Handshake) error   { return nil }

// BenchmarkObserve measures the steady-state hot path: an existing series is
// looked up and incremented in place. It must report 0 allocs/op.
func BenchmarkObserve(b *testing.B) {
	r := newBenchRecorder()
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		r.Observe(benchRecords[i&(len(benchRecords)-1)])
		i++
	}
}

// BenchmarkObserveParallel measures the hot path under concurrent load; N
// shards should keep goroutines off a single mutex. Also 0 allocs/op.
func BenchmarkObserveParallel(b *testing.B) {
	r := newBenchRecorder()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			r.Observe(benchRecords[i&(len(benchRecords)-1)])
			i++
		}
	})
}
