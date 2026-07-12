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

	row, err := summaryToRow(tenant.MustParse("dev-tenant"), "checkout-api", "pod-1", s, start, end)
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
	// Sketch keys sorted ascending.
	require.Len(t, row.Sketch, 2)
	assert.Equal(t, int32(10), row.Sketch[0].Index)
	assert.Equal(t, int32(30), row.Sketch[1].Index)
}

func TestSummaryToRowNil(t *testing.T) {
	_, err := summaryToRow(tenant.MustParse("t"), "s", "i", nil, time.Now(), time.Now())
	assert.Error(t, err)
}
