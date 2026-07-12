package storage

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBucketValueMatchesFrozenFormula(t *testing.T) {
	// value(0) = 2 * 1.01^0 / 2.01 = 2/2.01.
	assert.InDelta(t, 2.0/2.01, bucketValue(0), 1e-12)
	// value(1) = 2 * 1.01 / 2.01.
	assert.InDelta(t, 2*1.01/2.01, bucketValue(1), 1e-12)
	// Monotonic increasing in index.
	assert.Less(t, bucketValue(10), bucketValue(11))
	assert.Less(t, bucketValue(-1), bucketValue(0))
}

func TestQuantileFromBucketsConvention(t *testing.T) {
	// Buckets: index 10 has 1 count, index 20 has 1, index 30 has 8. total=10.
	buckets := map[int32]uint64{10: 1, 20: 1, 30: 8}

	tests := []struct {
		name string
		q    float64
		// expected rank -> bucket index whose cumulative count first reaches it.
		wantIndex int32
	}{
		// rank = ceil(0.05*10)=1 -> cumulative hits 1 at index 10.
		{"p05 first bucket", 0.05, 10},
		// rank = ceil(0.10*10)=1 -> still index 10.
		{"p10 first bucket", 0.10, 10},
		// rank = ceil(0.20*10)=2 -> cumulative 1 then 2 at index 20.
		{"p20 second bucket", 0.20, 20},
		// rank = ceil(0.50*10)=5 -> cumulative 1,2,10; 10>=5 at index 30.
		{"p50 third bucket", 0.50, 30},
		// rank = ceil(0.95*10)=10 -> index 30.
		{"p95 third bucket", 0.95, 30},
		// rank = ceil(0.99*10)=10 -> index 30.
		{"p99 third bucket", 0.99, 30},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := QuantileFromBuckets(buckets, tc.q)
			assert.InDelta(t, bucketValue(tc.wantIndex), got, 1e-12)
		})
	}
}

func TestQuantileEmptySketch(t *testing.T) {
	assert.Equal(t, 0.0, QuantileFromBuckets(nil, 0.5))
	assert.Equal(t, 0.0, QuantileFromBuckets(map[int32]uint64{}, 0.99))
}

func TestQuantileSingleBucket(t *testing.T) {
	buckets := map[int32]uint64{42: 100}
	for _, q := range []float64{0.5, 0.95, 0.99, 0.01} {
		assert.InDelta(t, bucketValue(42), QuantileFromBuckets(buckets, q), 1e-12)
	}
}

func TestQuantileRankClamp(t *testing.T) {
	// q=1.0 -> rank = ceil(1.0*total) = total, must return the last bucket.
	buckets := map[int32]uint64{1: 5, 2: 5}
	assert.InDelta(t, bucketValue(2), QuantileFromBuckets(buckets, 1.0), 1e-12)
	// q rounding: total=3, q=0.34 -> ceil(1.02)=2.
	b3 := map[int32]uint64{1: 1, 2: 1, 3: 1}
	assert.InDelta(t, bucketValue(2), QuantileFromBuckets(b3, 0.34), 1e-12)
}

func TestQuantileWithinRelativeErrorBound(t *testing.T) {
	// A DDSketch with gamma=1.01 guarantees ~1% relative error. Build buckets
	// from known latencies and assert the recovered p95 is within the bound of
	// the true p95.
	latencies := make([]float64, 0, 1000)
	buckets := map[int32]uint64{}
	for i := range 1000 {
		// spread 1ms..1s.
		v := 0.001 + float64(i)*(1.0-0.001)/999.0
		latencies = append(latencies, v)
		idx := indexOfValue(v)
		buckets[idx]++
	}
	// true p95 by the same rank convention over raw values.
	// rank = ceil(0.95*1000)=950 (1-based) -> latencies[949].
	trueP95 := latencies[949]
	got := QuantileFromBuckets(buckets, 0.95)
	relErr := math.Abs(got-trueP95) / trueP95
	assert.Less(t, relErr, 0.02, "p95 relative error within DDSketch bound")
}

// indexOfValue is the inverse mapping used only by the test to place a latency
// into its bucket: i = ceil(log_gamma((gamma+1)*v/2)). It mirrors the client's
// bucket assignment for value(i).
func indexOfValue(v float64) int32 {
	return int32(math.Ceil(math.Log(SketchGammaPlusOne*v/2) / math.Log(SketchGamma)))
}
