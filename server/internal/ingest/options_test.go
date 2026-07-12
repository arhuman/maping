package ingest

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mapingv1 "github.com/arhuman/maping/proto/maping/v1"
)

// --- allowFunc ---

// TestAllowFunc_Allow verifies that allowFunc.allow delegates to the wrapped func.
func TestAllowFunc_Allow(t *testing.T) {
	tests := []struct {
		name   string
		result bool
	}{
		{"allow returns true", true},
		{"allow returns false", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fn := allowFunc(func(_ string) bool { return tt.result })
			got := fn.allow("some-tenant")
			assert.Equal(t, tt.result, got)
		})
	}
}

// TestAllowFunc_TenantPropagated verifies allowFunc forwards the tenant string unchanged.
func TestAllowFunc_TenantPropagated(t *testing.T) {
	const want = "tenant-abc"
	var got string
	fn := allowFunc(func(tenant string) bool {
		got = tenant
		return true
	})
	fn.allow(want)
	assert.Equal(t, want, got, "tenant string must be forwarded to the wrapped func")
}

// --- WithLimiter ---

// TestWithLimiter_NilIgnored verifies that a nil allow func leaves the default limiter.
func TestWithLimiter_NilIgnored(t *testing.T) {
	resolver := NewStaticKeyResolver(map[string]string{"dev-key": "dev-tenant"})
	h := NewHandler(resolver, &fakeSink{}, nil, WithLimiter(nil))
	// The default limiter is a *tenantLimiter; it should still be present and functional.
	assert.NotNil(t, h.limiter)
}

// TestWithLimiter_DenyAllBlocksUpload verifies that WithLimiter wires the supplied
// allow function: a deny-all limiter causes Upload to return CodeResourceExhausted.
func TestWithLimiter_DenyAllBlocksUpload(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	resolver := NewStaticKeyResolver(map[string]string{"dev-key": "dev-tenant"})
	sink := &fakeSink{}
	h := NewHandler(resolver, sink, nil,
		WithLimiter(func(_ string) bool { return false }), // always deny
	)
	h.now = func() time.Time { return now }

	msg := &mapingv1.UploadRequest{Envelope: &mapingv1.Envelope{Service: "s"}}
	_, err := h.Upload(context.Background(), withBearer(msg, "dev-key"))
	require.Error(t, err)
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err),
		"deny-all limiter must produce CodeResourceExhausted")
	assert.Equal(t, 0, sink.count(), "denied request must not reach the sink")
}

// TestWithLimiter_AllowAllPassesUpload verifies that WithLimiter with an allow-all
// function does not block authenticated, in-band summaries.
func TestWithLimiter_AllowAllPassesUpload(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	resolver := NewStaticKeyResolver(map[string]string{"dev-key": "dev-tenant"})
	sink := &fakeSink{}
	h := NewHandler(resolver, sink, nil,
		WithLimiter(func(_ string) bool { return true }), // always allow
	)
	h.now = func() time.Time { return now }

	msg := &mapingv1.UploadRequest{
		Envelope: &mapingv1.Envelope{Service: "svc"},
		Summaries: []*mapingv1.Summary{
			{
				WindowStartMs: now.Add(-5 * time.Second).UnixMilli(),
				WindowEndMs:   now.UnixMilli(),
				Count:         1,
			},
		},
	}
	resp, err := h.Upload(context.Background(), withBearer(msg, "dev-key"))
	require.NoError(t, err)
	assert.True(t, resp.Msg.GetAccepted())
	assert.Equal(t, 1, sink.count(), "allow-all limiter must not block an in-band summary")
}

// --- WithCardinality ---

// TestWithCardinality_BothNilIsNoop verifies that WithCardinality with nil functions
// leaves cardinality and cap as nil (no guard).
func TestWithCardinality_BothNilIsNoop(t *testing.T) {
	resolver := NewStaticKeyResolver(map[string]string{"dev-key": "dev-tenant"})
	h := NewHandler(resolver, &fakeSink{}, nil, WithCardinality(nil, nil))
	assert.Nil(t, h.cardinality)
	assert.Nil(t, h.cap)
}

// TestWithCardinality_WiredAllowBlocks verifies that when WithCardinality is wired
// with a deny-all cardinalityFunc, Upload drops all summaries as cardinality-rejected.
func TestWithCardinality_WiredAllowBlocks(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	resolver := NewStaticKeyResolver(map[string]string{"dev-key": "dev-tenant"})
	sink := &fakeSink{}

	// deny-all cardinality: never allow a series.
	denyCard := cardinalityFunc(func(_, _ string, _ int) (allowed, frozen bool) {
		return false, true
	})
	capFn := capProvider(func(_ context.Context, _ string) int { return 10 })

	h := NewHandler(resolver, sink, nil, WithCardinality(denyCard, capFn))
	h.now = func() time.Time { return now }
	assert.NotNil(t, h.cardinality, "WithCardinality must wire the cardinality func")
	assert.NotNil(t, h.cap, "WithCardinality must wire the cap provider")

	msg := &mapingv1.UploadRequest{
		Envelope: &mapingv1.Envelope{Service: "svc"},
		Summaries: []*mapingv1.Summary{
			{
				WindowStartMs: now.Add(-5 * time.Second).UnixMilli(),
				WindowEndMs:   now.UnixMilli(),
				Count:         1,
			},
		},
	}
	resp, err := h.Upload(context.Background(), withBearer(msg, "dev-key"))
	require.NoError(t, err)
	assert.True(t, resp.Msg.GetAccepted())
	assert.Equal(t, uint64(1), resp.Msg.GetRejectedSummaries(),
		"deny-all cardinality func must cause all summaries to be rejected")
	assert.Equal(t, 0, sink.count(), "rejected summaries must not reach the sink")
}

// TestWithCardinality_WiredAllowAccepts verifies that an allow-all cardinalityFunc
// lets summaries through.
func TestWithCardinality_WiredAllowAccepts(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	resolver := NewStaticKeyResolver(map[string]string{"dev-key": "dev-tenant"})
	sink := &fakeSink{}

	allowCard := cardinalityFunc(func(_, _ string, _ int) (allowed, frozen bool) {
		return true, false
	})
	capFn := capProvider(func(_ context.Context, _ string) int { return 100 })

	h := NewHandler(resolver, sink, nil, WithCardinality(allowCard, capFn))
	h.now = func() time.Time { return now }

	msg := &mapingv1.UploadRequest{
		Envelope: &mapingv1.Envelope{Service: "svc"},
		Summaries: []*mapingv1.Summary{
			{
				WindowStartMs: now.Add(-5 * time.Second).UnixMilli(),
				WindowEndMs:   now.UnixMilli(),
				Count:         2,
			},
		},
	}
	resp, err := h.Upload(context.Background(), withBearer(msg, "dev-key"))
	require.NoError(t, err)
	assert.Equal(t, uint64(0), resp.Msg.GetRejectedSummaries())
	assert.Equal(t, 1, sink.count(), "allow-all cardinality must not drop summaries")
}
