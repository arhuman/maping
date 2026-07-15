package maping

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sleepRT is a fake base transport that spends a fixed duration per round trip,
// so the wrapper's measured downstream time is deterministically non-zero.
type sleepRT struct{ d time.Duration }

func (s sleepRT) RoundTrip(*http.Request) (*http.Response, error) {
	time.Sleep(s.d)
	return &http.Response{StatusCode: 200, Body: http.NoBody, Header: make(http.Header)}, nil
}

func TestRoundTripperAccumulatesDownstream(t *testing.T) {
	rt := NewRoundTripper(sleepRT{d: 5 * time.Millisecond})
	ctx := WithDownstreamTracking(context.Background())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://x", nil)
	require.NoError(t, err)
	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	resp.Body.Close()

	afterOne := DownstreamElapsed(ctx)
	assert.Positive(t, afterOne, "one round trip must record some downstream time")

	// A second outbound call on the same context accumulates on top of the first.
	req2, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://x", nil)
	require.NoError(t, err)
	resp2, err := rt.RoundTrip(req2)
	require.NoError(t, err)
	resp2.Body.Close()
	assert.GreaterOrEqual(t, DownstreamElapsed(ctx), afterOne, "downstream time must accumulate across calls")
}

func TestRoundTripperNoTrackingIsNoop(t *testing.T) {
	rt := NewRoundTripper(sleepRT{d: time.Millisecond})
	// A plain context (no accumulator installed): the round trip still works and
	// DownstreamElapsed reports zero.
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://x", nil)
	require.NoError(t, err)
	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)
	assert.Zero(t, DownstreamElapsed(context.Background()))
}

// TestObserveEmitsDownstream checks Observe threads the per-request downstream
// duration into the built Summary's summed downstream field.
func TestObserveEmitsDownstream(t *testing.T) {
	r := newTestRecorder(&fakeTransport{})
	r.Observe(Record{Method: "GET", RouteTemplate: "/x", Status: 200, Duration: 30 * time.Millisecond, DownstreamDuration: 20 * time.Millisecond})
	r.Observe(Record{Method: "GET", RouteTemplate: "/x", Status: 200, Duration: 10 * time.Millisecond, DownstreamDuration: 5 * time.Millisecond})

	req := r.buildRequest(r.swapShards(), time.Now())
	require.Len(t, req.Summaries, 1)
	assert.Equal(t, uint64((25 * time.Millisecond).Nanoseconds()), req.Summaries[0].GetSumDownstreamDurationNs())
}
