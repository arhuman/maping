package ingest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/arhuman/maping/server/internal/storage"

	mapingv1 "github.com/arhuman/maping/proto/maping/v1"
)

// fakeSink records enqueued rows and can be forced to fail.
type fakeSink struct {
	mu      sync.Mutex
	rows    []storage.Row
	failNow bool
}

func (f *fakeSink) Enqueue(row storage.Row) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNow {
		return errors.New("sink closed")
	}
	f.rows = append(f.rows, row)
	return nil
}

func (f *fakeSink) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.rows)
}

func newTestHandler(t *testing.T, sink RowSink, now time.Time) *Handler {
	t.Helper()
	resolver := NewStaticKeyResolver(map[string]string{"dev-key": "dev-tenant"})
	h := NewHandler(resolver, sink, nil)
	h.now = func() time.Time { return now }
	return h
}

func withBearer[T any](msg *T, key string) *connect.Request[T] {
	req := connect.NewRequest(msg)
	if key != "" {
		req.Header().Set("Authorization", "Bearer "+key)
	}
	return req
}

func TestExtractKey(t *testing.T) {
	req := connect.NewRequest(&mapingv1.Handshake{})
	req.Header().Set("Authorization", "Bearer abc123")
	assert.Equal(t, "abc123", extractKey(req.Header()))

	req2 := connect.NewRequest(&mapingv1.Handshake{})
	req2.Header().Set("X-Maping-Key", "xyz")
	assert.Equal(t, "xyz", extractKey(req2.Header()))

	req3 := connect.NewRequest(&mapingv1.Handshake{})
	assert.Equal(t, "", extractKey(req3.Header()))
}

func TestRegisterUnknownKeyUnauthenticated(t *testing.T) {
	h := newTestHandler(t, &fakeSink{}, time.Now().UTC())
	_, err := h.Register(context.Background(), withBearer(&mapingv1.Handshake{Service: "s"}, "bad-key"))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestRegisterMissingKeyUnauthenticated(t *testing.T) {
	h := newTestHandler(t, &fakeSink{}, time.Now().UTC())
	_, err := h.Register(context.Background(), connect.NewRequest(&mapingv1.Handshake{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestRegisterAccepted(t *testing.T) {
	h := newTestHandler(t, &fakeSink{}, time.Now().UTC())
	resp, err := h.Register(context.Background(), withBearer(&mapingv1.Handshake{Service: "s"}, "dev-key"))
	require.NoError(t, err)
	assert.True(t, resp.Msg.GetAccepted())
}

func TestUploadStoresInBandSummaries(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	sink := &fakeSink{}
	h := newTestHandler(t, sink, now)

	msg := &mapingv1.UploadRequest{
		Envelope: &mapingv1.Envelope{Service: "checkout-api", Instance: "pod-1"},
		Summaries: []*mapingv1.Summary{
			{
				WindowStartMs: now.Add(-5 * time.Second).UnixMilli(),
				WindowEndMs:   now.UnixMilli(),
				Method:        "GET",
				RouteTemplate: "/x",
				StatusClass:   mapingv1.StatusClass_STATUS_CLASS_2XX,
				Count:         10,
				LatencySketch: map[int32]uint64{5: 10},
			},
		},
	}
	resp, err := h.Upload(context.Background(), withBearer(msg, "dev-key"))
	require.NoError(t, err)
	assert.True(t, resp.Msg.GetAccepted())
	assert.Equal(t, uint64(0), resp.Msg.GetRejectedSummaries())
	require.Equal(t, 1, sink.count())
	assert.Equal(t, "dev-tenant", sink.rows[0].Tenant.String())
	assert.Equal(t, "checkout-api", sink.rows[0].Service)
}

func TestUploadDropsOutOfBandSummaries(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	sink := &fakeSink{}
	h := newTestHandler(t, sink, now)

	msg := &mapingv1.UploadRequest{
		Envelope: &mapingv1.Envelope{Service: "s"},
		Summaries: []*mapingv1.Summary{
			{ // in-band
				WindowStartMs: now.Add(-5 * time.Second).UnixMilli(),
				WindowEndMs:   now.UnixMilli(),
				Count:         1,
			},
			{ // 11 minutes future skew -> dropped
				WindowStartMs: now.Add(11 * time.Minute).UnixMilli(),
				WindowEndMs:   now.Add(11*time.Minute + 5*time.Second).UnixMilli(),
				Count:         1,
			},
		},
	}
	resp, err := h.Upload(context.Background(), withBearer(msg, "dev-key"))
	require.NoError(t, err)
	assert.True(t, resp.Msg.GetAccepted())
	assert.Equal(t, uint64(1), resp.Msg.GetRejectedSummaries(), "out-of-band summary dropped and counted")
	assert.Equal(t, 1, sink.count(), "only the in-band summary stored")
}

func TestUploadUnauthenticated(t *testing.T) {
	h := newTestHandler(t, &fakeSink{}, time.Now().UTC())
	_, err := h.Upload(context.Background(), withBearer(&mapingv1.UploadRequest{}, "bad"))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestUploadRateLimited(t *testing.T) {
	now := time.Now().UTC()
	h := newTestHandler(t, &fakeSink{}, now)
	// Force a tight limiter so the second call is throttled.
	h.limiter = newTenantLimiter(1, 1)

	msg := &mapingv1.UploadRequest{Envelope: &mapingv1.Envelope{Service: "s"}}
	_, err := h.Upload(context.Background(), withBearer(msg, "dev-key"))
	require.NoError(t, err)

	_, err = h.Upload(context.Background(), withBearer(msg, "dev-key"))
	require.Error(t, err)
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
}

func TestUploadPayloadLimit(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)

	// A small in-band summary batch used across cases; its proto.Size drives the
	// under/over decision relative to the injected cap.
	newMsg := func() *mapingv1.UploadRequest {
		return &mapingv1.UploadRequest{
			Envelope: &mapingv1.Envelope{Service: "checkout-api", Instance: "pod-1"},
			Summaries: []*mapingv1.Summary{
				{
					WindowStartMs: now.Add(-5 * time.Second).UnixMilli(),
					WindowEndMs:   now.UnixMilli(),
					Method:        "GET",
					RouteTemplate: "/x",
					StatusClass:   mapingv1.StatusClass_STATUS_CLASS_2XX,
					Count:         10,
					LatencySketch: map[int32]uint64{5: 10},
				},
			},
		}
	}
	msgSize := int64(proto.Size(newMsg()))

	tests := []struct {
		name       string
		option     Option // nil = no WithPayloadLimit at all
		wantErr    bool
		wantStored int
	}{
		{
			name:       "no option runs no check",
			option:     nil,
			wantErr:    false,
			wantStored: 1,
		},
		{
			name:       "under limit passes",
			option:     WithPayloadLimit(func(context.Context, string) int64 { return msgSize + 1 }),
			wantErr:    false,
			wantStored: 1,
		},
		{
			name:       "over limit rejected",
			option:     WithPayloadLimit(func(context.Context, string) int64 { return msgSize - 1 }),
			wantErr:    true,
			wantStored: 0,
		},
		{
			name:       "cap zero disables check",
			option:     WithPayloadLimit(func(context.Context, string) int64 { return 0 }),
			wantErr:    false,
			wantStored: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sink := &fakeSink{}
			resolver := NewStaticKeyResolver(map[string]string{"dev-key": "dev-tenant"})
			var opts []Option
			if tt.option != nil {
				opts = append(opts, tt.option)
			}
			h := NewHandler(resolver, sink, nil, opts...)
			h.now = func() time.Time { return now }

			resp, err := h.Upload(context.Background(), withBearer(newMsg(), "dev-key"))
			if tt.wantErr {
				require.Error(t, err)
				assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
			} else {
				require.NoError(t, err)
				assert.True(t, resp.Msg.GetAccepted())
			}
			assert.Equal(t, tt.wantStored, sink.count())
		})
	}
}

func TestUploadSinkFailureCountsRejected(t *testing.T) {
	now := time.Now().UTC()
	sink := &fakeSink{failNow: true}
	h := newTestHandler(t, sink, now)

	msg := &mapingv1.UploadRequest{
		Envelope: &mapingv1.Envelope{Service: "s"},
		Summaries: []*mapingv1.Summary{
			{WindowStartMs: now.Add(-time.Second).UnixMilli(), WindowEndMs: now.UnixMilli(), Count: 1},
		},
	}
	resp, err := h.Upload(context.Background(), withBearer(msg, "dev-key"))
	require.NoError(t, err)
	assert.Equal(t, uint64(1), resp.Msg.GetRejectedSummaries())
}
