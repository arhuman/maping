package maping

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"
)

// downstreamKeyType is an unexported context-key type so the accumulator handle
// cannot collide with any other package's context values.
type downstreamKeyType struct{}

var downstreamKey = downstreamKeyType{}

// downstreamAccumulator sums, across a single request, the time spent waiting on
// downstream calls. A pointer to it lives in the request context so the
// RoundTripper (which sees a derived context on each outbound request) can add to
// the same counter the adapter later reads. It is atomic because a handler may
// fan out to several downstream calls concurrently.
type downstreamAccumulator struct {
	ns atomic.Int64
}

// WithDownstreamTracking installs a fresh downstream-time accumulator on ctx and
// returns the derived context. An adapter calls it once at request start; every
// outbound request whose context descends from this one then folds its round-trip
// time into the same counter (see NewRoundTripper), which DownstreamElapsed reads
// back at request end. Without this call the RoundTripper is a no-op, so wiring is
// opt-in and costs nothing when unused.
func WithDownstreamTracking(ctx context.Context) context.Context {
	return context.WithValue(ctx, downstreamKey, &downstreamAccumulator{})
}

// DownstreamElapsed returns the total downstream time accumulated on ctx since
// WithDownstreamTracking installed the counter, or 0 when none was installed or
// no downstream call was recorded.
func DownstreamElapsed(ctx context.Context) time.Duration {
	if acc, ok := ctx.Value(downstreamKey).(*downstreamAccumulator); ok {
		return time.Duration(acc.ns.Load())
	}
	return 0
}

// addDownstream folds one downstream call's duration into the accumulator on ctx,
// if one is present. It is safe for concurrent callers.
func addDownstream(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	if acc, ok := ctx.Value(downstreamKey).(*downstreamAccumulator); ok {
		acc.ns.Add(int64(d))
	}
}

// RoundTripper wraps an http.RoundTripper and folds each outbound request's
// round-trip time into the downstream accumulator carried by the request context
// (installed by WithDownstreamTracking). A host sets it as the Transport of the
// http.Client it uses for downstream calls, and propagates the inbound request
// context to those calls, so mAPI-ng can split an endpoint's own time from time
// spent blocked on a dependency. When the context carries no accumulator it adds
// nothing and simply delegates, so it is safe to use everywhere.
type RoundTripper struct {
	base http.RoundTripper
}

// NewRoundTripper wraps base so outbound round-trip time is attributed to the
// caller's request. A nil base falls back to http.DefaultTransport, matching the
// convention of the standard library's own transports.
func NewRoundTripper(base http.RoundTripper) *RoundTripper {
	return &RoundTripper{base: base}
}

// RoundTrip measures the wrapped round trip and adds its duration to the request
// context's downstream accumulator. The duration is recorded even on error, since
// a slow failing dependency is exactly the downstream time worth surfacing.
func (rt *RoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	base := rt.base
	if base == nil {
		base = http.DefaultTransport
	}
	start := time.Now()
	resp, err := base.RoundTrip(req)
	addDownstream(req.Context(), time.Since(start))
	return resp, err
}
