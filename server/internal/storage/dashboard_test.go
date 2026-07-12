package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildEndpointDetail exercises the pure mapper: the index->latency
// conversion, the per-class rollup, the error-rate math, and the status-code
// map, without a live ClickHouse. The percentiles must match the frozen Go
// oracle over the same merged sketch.
func TestBuildEndpointDetail(t *testing.T) {
	t.Run("full", func(t *testing.T) {
		// total = 2 + 1 + 3 + 4 + 0 = 10; errors = 4xx+5xx+no_status = 3+4+0 = 7.
		ks := []int32{10, 20, 30}
		vs := []uint64{1, 2, 7} // sketch total = 10, matches request total.
		codeKeys := []uint32{200, 404, 500}
		codeVals := []uint64{2, 3, 4}

		d := buildEndpointDetail(10, 2, 1, 3, 4, 0, ks, vs, codeKeys, codeVals)

		require.Equal(t, uint64(10), d.Count)
		assert.InDelta(t, 7.0/10.0, d.ErrorRate, 1e-9)

		// Histogram bars map index -> value(i) seconds and keep counts aligned.
		require.Len(t, d.Histogram, 3)
		assert.InDelta(t, bucketValue(10), d.Histogram[0].LatencySeconds, 1e-12)
		assert.Equal(t, uint64(7), d.Histogram[2].Count)

		// Percentiles must equal the frozen oracle on the same buckets.
		buckets := map[int32]uint64{10: 1, 20: 2, 30: 7}
		assert.InDelta(t, QuantileFromBuckets(buckets, 0.50), d.P50, 1e-12)
		assert.InDelta(t, QuantileFromBuckets(buckets, 0.95), d.P95, 1e-12)
		assert.InDelta(t, QuantileFromBuckets(buckets, 0.99), d.P99, 1e-12)

		// Class breakdown is in the fixed display order.
		require.Len(t, d.StatusClasses, 5)
		assert.Equal(t, StatusClassCount{Class: "2xx", Count: 2}, d.StatusClasses[0])
		assert.Equal(t, StatusClassCount{Class: "4xx", Count: 3}, d.StatusClasses[2])
		assert.Equal(t, StatusClassCount{Class: "no_status", Count: 0}, d.StatusClasses[4])

		// Exact status-code map.
		assert.Equal(t, map[uint32]uint64{200: 2, 404: 3, 500: 4}, d.StatusCodes)
	})

	t.Run("empty is zero-valued and safe", func(t *testing.T) {
		d := buildEndpointDetail(0, 0, 0, 0, 0, 0, nil, nil, nil, nil)
		assert.Equal(t, uint64(0), d.Count)
		assert.Zero(t, d.ErrorRate)
		assert.Zero(t, d.P50)
		assert.Empty(t, d.Histogram)
		assert.Equal(t, map[uint32]uint64{}, d.StatusCodes)
		// Classes still render (all zero), so the breakdown UI has stable rows.
		require.Len(t, d.StatusClasses, 5)
	})

	t.Run("no_status counts as error", func(t *testing.T) {
		// Only a no_status class: error rate is 100%.
		d := buildEndpointDetail(4, 0, 0, 0, 0, 4, []int32{5}, []uint64{4}, nil, nil)
		assert.InDelta(t, 1.0, d.ErrorRate, 1e-9)
	})

	t.Run("mismatched sketch arrays are ignored, not panicked", func(t *testing.T) {
		// Defensive: a malformed ks/vs pairing must not build bars or panic.
		d := buildEndpointDetail(3, 3, 0, 0, 0, 0, []int32{1, 2}, []uint64{1}, nil, nil)
		assert.Empty(t, d.Histogram)
		assert.Zero(t, d.P50)
	})
}
