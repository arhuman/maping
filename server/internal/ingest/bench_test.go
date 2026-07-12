package ingest

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/arhuman/maping/server/internal/storage"

	mapingv1 "github.com/arhuman/maping/proto/maping/v1"
)

// Bench fixtures for the ingest hot path (Upload -> authenticate -> timestamp
// policy -> summaryToRow -> Enqueue). The client uploads once per flush window,
// so this Connect handler is the highest-QPS server surface; these benchmarks
// lock in its per-request cost and allocation profile.

const (
	benchKey    = "dev-key"
	benchTenant = "dev-tenant"
)

// benchNow is the fixed server clock; bench summaries are stamped in-band
// relative to it so every summary is accepted (no skew rejections) and the
// timed loop measures the accept path.
var benchNow = time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)

// benchSink is a no-op RowSink: it discards every row so the benchmark measures
// the handler's own work rather than the accumulation cost of a recording sink.
type benchSink struct{}

func (benchSink) Enqueue(storage.Row) error { return nil }

// newBenchHandler builds a handler on the discarding sink with the fixed clock.
// The token bucket is sized so large it never throttles the timed loop, while
// the real per-tenant limiter lookup still runs on the hot path.
func newBenchHandler() *Handler {
	resolver := NewStaticKeyResolver(map[string]string{benchKey: benchTenant})
	h := NewHandler(resolver, benchSink{}, nil)
	h.now = func() time.Time { return benchNow }
	h.limiter = newTenantLimiter(1e9, 1<<30)
	return h
}

// benchSummaries builds n distinct in-band summaries spread across methods,
// routes, and status classes so summaryToRow exercises varied series keys and
// the map-sorting conversions rather than one repeated row.
func benchSummaries(n int) []*mapingv1.Summary {
	methods := []string{"GET", "POST", "PUT", "DELETE"}
	routes := []string{"/users/:id", "/orders", "/orders/:id", "/products", "/login", "/health", "/cart/:id", "/search"}
	classes := []mapingv1.StatusClass{
		mapingv1.StatusClass_STATUS_CLASS_2XX,
		mapingv1.StatusClass_STATUS_CLASS_4XX,
		mapingv1.StatusClass_STATUS_CLASS_5XX,
	}
	out := make([]*mapingv1.Summary, n)
	for i := range out {
		out[i] = &mapingv1.Summary{
			WindowStartMs:       benchNow.Add(-10 * time.Second).UnixMilli(),
			WindowEndMs:         benchNow.UnixMilli(),
			Method:              methods[i%len(methods)],
			RouteTemplate:       routes[i%len(routes)],
			StatusClass:         classes[i%len(classes)],
			Count:               uint64(10 + i),
			SumDurationNs:       1_500_000,
			ReqBytes:            256,
			RespBytes:           1024,
			LatencySketch:       map[int32]uint64{5: 7, 6: 3, 7: 1},
			StatusCodeBreakdown: map[uint32]uint64{200: 8, 404: 2},
		}
	}
	return out
}

func benchRequest(n int) *connect.Request[mapingv1.UploadRequest] {
	return withBearer(&mapingv1.UploadRequest{
		Envelope:  &mapingv1.Envelope{Service: "checkout-api", Instance: "pod-1"},
		Summaries: benchSummaries(n),
	}, benchKey)
}

func benchmarkUpload(b *testing.B, n int) {
	h := newBenchHandler()
	req := benchRequest(n)
	ctx := context.Background()

	b.ReportAllocs()
	for b.Loop() {
		resp, err := h.Upload(ctx, req)
		if err != nil {
			b.Fatal(err)
		}
		if resp.Msg.GetRejectedSummaries() != 0 {
			b.Fatalf("unexpected rejections: %d", resp.Msg.GetRejectedSummaries())
		}
	}
}

// BenchmarkUploadSingleSummary is the smallest realistic batch: one series.
func BenchmarkUploadSingleSummary(b *testing.B) { benchmarkUpload(b, 1) }

// BenchmarkUploadBatch is a representative flush: many distinct series in one
// request, so the per-summary conversion cost dominates the fixed auth cost.
func BenchmarkUploadBatch(b *testing.B) { benchmarkUpload(b, 64) }

// BenchmarkUploadParallel measures the handler under concurrent uploads (Connect
// serves one goroutine per request), exercising the limiter mutex under load.
func BenchmarkUploadParallel(b *testing.B) {
	h := newBenchHandler()
	req := benchRequest(64)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := h.Upload(ctx, req); err != nil {
				b.Fatal(err)
			}
		}
	})
}
