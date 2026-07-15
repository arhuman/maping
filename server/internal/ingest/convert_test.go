package ingest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arhuman/maping/server/internal/tenant"

	mapingv1 "github.com/arhuman/maping/proto/maping/v1"
)

func TestStatusClassName(t *testing.T) {
	tests := []struct {
		sc   mapingv1.StatusClass
		want string
	}{
		{mapingv1.StatusClass_STATUS_CLASS_UNSPECIFIED, "STATUS_CLASS_UNSPECIFIED"},
		{mapingv1.StatusClass_STATUS_CLASS_2XX, "STATUS_CLASS_2XX"},
		{mapingv1.StatusClass_STATUS_CLASS_3XX, "STATUS_CLASS_3XX"},
		{mapingv1.StatusClass_STATUS_CLASS_4XX, "STATUS_CLASS_4XX"},
		{mapingv1.StatusClass_STATUS_CLASS_5XX, "STATUS_CLASS_5XX"},
		{mapingv1.StatusClass_STATUS_CLASS_NO_STATUS, "STATUS_CLASS_NO_STATUS"},
		{mapingv1.StatusClass(99), "STATUS_CLASS_UNSPECIFIED"}, // unknown -> unspecified
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			assert.Equal(t, tc.want, statusClassName(tc.sc))
		})
	}
}

func TestApplyTimestampPolicy(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	nowMs := now.UnixMilli()

	tests := []struct {
		name         string
		endMs        int64
		wantAccepted bool
	}{
		{"exactly now", nowMs, true},
		{"small past drift 1m", nowMs - 60_000, true},
		{"small future drift 1m", nowMs + 60_000, true},
		{"edge past 10m", nowMs - 10*60_000, true},
		{"edge future 10m", nowMs + 10*60_000, true},
		{"past skew 11m dropped", nowMs - 11*60_000, false},
		{"future skew 11m dropped", nowMs + 11*60_000, false},
		{"far past dropped", nowMs - 24*60*60_000, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			startMs := tc.endMs - 5000
			d := applyTimestampPolicy(startMs, tc.endMs, now)
			assert.Equal(t, tc.wantAccepted, d.accepted)
			if tc.wantAccepted {
				// In-band drift kept as-is, not clamped onto now.
				assert.Equal(t, time.UnixMilli(tc.endMs).UTC(), d.end)
				assert.Equal(t, time.UnixMilli(startMs).UTC(), d.start)
			}
		})
	}
}

func TestSummaryToRow(t *testing.T) {
	start := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	end := start.Add(5 * time.Second)
	s := &mapingv1.Summary{
		Method:        "GET",
		RouteTemplate: "/users/:id",
		StatusClass:   mapingv1.StatusClass_STATUS_CLASS_2XX,
		Count:         42,
		SumDurationNs: 1000,
		ReqBytes:      500,
		RespBytes:     900,
		LatencySketch: map[int32]uint64{30: 40, 10: 2},
		StatusCodeBreakdown: map[uint32]uint64{
			200: 40, 201: 2,
		},
	}

	instanceStart := time.Date(2026, 7, 9, 11, 30, 0, 0, time.UTC)
	row, err := summaryToRow(tenant.MustParse("dev-tenant"), "checkout-api", "pod-1",
		"v1.2.3", "abc123sha", "prod", "eu-west-1", instanceStart,
		s, start, end)
	require.NoError(t, err)

	assert.Equal(t, "dev-tenant", row.Tenant.String())
	assert.Equal(t, "checkout-api", row.Service)
	assert.Equal(t, "pod-1", row.Instance)
	assert.Equal(t, "GET", row.Method)
	assert.Equal(t, "/users/:id", row.RouteTemplate)
	assert.Equal(t, "STATUS_CLASS_2XX", row.StatusClass)
	assert.Equal(t, uint64(42), row.Count)
	assert.Equal(t, start, row.WindowStart)
	assert.Equal(t, end, row.WindowEnd)
	// Deploy identity from the Envelope lands in the Row.
	assert.Equal(t, "v1.2.3", row.DeployVersion)
	assert.Equal(t, "abc123sha", row.DeployID)
	assert.Equal(t, "prod", row.Environment)
	assert.Equal(t, "eu-west-1", row.Region)
	assert.Equal(t, instanceStart, row.InstanceStart)
	// Sketch keys sorted ascending.
	require.Len(t, row.Sketch, 2)
	assert.Equal(t, int32(10), row.Sketch[0].Index)
	assert.Equal(t, int32(30), row.Sketch[1].Index)
}

func TestSummaryToRowExemplars(t *testing.T) {
	start := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	end := start.Add(5 * time.Second)
	atMs := start.Add(time.Second).UnixMilli()
	s := &mapingv1.Summary{
		Method:        "GET",
		RouteTemplate: "/x",
		StatusClass:   mapingv1.StatusClass_STATUS_CLASS_5XX,
		Count:         3,
		MaxDurationNs: 987_654_321,
		Exemplars: []*mapingv1.Exemplar{
			{
				AtMs:       atMs,
				DurationNs: 987_654_321,
				StatusCode: 503,
				TraceId:    "4bf92f3577b34da6a3ce929d0e0e4736",
				SpanId:     "00f067aa0ba902b7",
				RequestId:  "req-1",
			},
		},
	}

	row, err := summaryToRow(tenant.MustParse("t"), "svc", "inst",
		"", "", "", "", time.Time{}, s, start, end)
	require.NoError(t, err)

	assert.Equal(t, uint64(987_654_321), row.MaxDurationNs)
	require.Len(t, row.Exemplars, 1)
	ex := row.Exemplars[0]
	assert.Equal(t, time.UnixMilli(atMs).UTC(), ex.At)
	assert.Equal(t, time.UTC, ex.At.Location(), "exemplar time must be UTC")
	assert.Equal(t, uint64(987_654_321), ex.DurationNs)
	assert.Equal(t, uint32(503), ex.StatusCode)
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", ex.TraceID)
	assert.Equal(t, "00f067aa0ba902b7", ex.SpanID)
	assert.Equal(t, "req-1", ex.RequestID)
}

func TestSummaryToRowNoExemplars(t *testing.T) {
	start := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	s := &mapingv1.Summary{Method: "GET", RouteTemplate: "/x"}
	row, err := summaryToRow(tenant.MustParse("t"), "svc", "inst",
		"", "", "", "", time.Time{}, s, start, start.Add(time.Second))
	require.NoError(t, err)
	assert.Equal(t, uint64(0), row.MaxDurationNs)
	assert.Empty(t, row.Exemplars)
}

func TestSummaryToRowNil(t *testing.T) {
	_, err := summaryToRow(tenant.MustParse("t"), "s", "i",
		"", "", "", "", time.Time{},
		nil, time.Now(), time.Now())
	assert.Error(t, err)
}

func TestInstanceWindowToRow(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	iw := &mapingv1.InstanceWindow{
		WindowStartMs:  now.Add(-10 * time.Second).UnixMilli(),
		WindowEndMs:    now.UnixMilli(),
		CpuNs:          1500,
		RssBytes:       2048,
		HeapAllocBytes: 1024,
		GcPauseNs:      300,
		Goroutines:     42,
	}
	row, ok := instanceWindowToRow(tenant.MustParse("dev-tenant"), "checkout-api", "pod-1", iw, now)
	require.True(t, ok)
	assert.Equal(t, "dev-tenant", row.Tenant.String())
	assert.Equal(t, "checkout-api", row.Service)
	assert.Equal(t, "pod-1", row.Instance)
	assert.Equal(t, uint64(1500), row.CPUNs)
	assert.Equal(t, uint64(2048), row.RSSBytes)
	assert.Equal(t, uint64(1024), row.HeapAllocBytes)
	assert.Equal(t, uint64(300), row.GCPauseNs)
	assert.Equal(t, uint64(42), row.Goroutines)
}

func TestInstanceWindowToRowRejectsSkewAndNil(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	// Out of the skew band: dropped, not clamped.
	stale := &mapingv1.InstanceWindow{
		WindowStartMs: now.Add(-2 * time.Hour).UnixMilli(),
		WindowEndMs:   now.Add(-time.Hour).UnixMilli(),
	}
	_, ok := instanceWindowToRow(tenant.MustParse("t"), "svc", "inst", stale, now)
	assert.False(t, ok, "an out-of-band window is rejected")

	_, ok = instanceWindowToRow(tenant.MustParse("t"), "svc", "inst", nil, now)
	assert.False(t, ok, "a nil window is rejected")
}
