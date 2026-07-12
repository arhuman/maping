package storage

import (
	"math"
	"sort"
)

// bucketValue returns the latency value in seconds represented by DDSketch
// bucket index i, using the FROZEN mapping shared with the client and the
// ClickHouse percentile SQL:
//
//	value(i) = 2 * pow(gamma, i) / (gamma + 1)   with gamma = 1.01
//
// This mirrors the SQL expression `2*pow(1.01, idx)/2.01`.
func bucketValue(i int32) float64 {
	return 2 * math.Pow(SketchGamma, float64(i)) / SketchGammaPlusOne
}

// QuantileFromBuckets computes a quantile from a merged DDSketch bucket map
// using the FROZEN convention that MUST match both the client and query.go's
// SQL:
//
//	total = sum of counts
//	rank  = clamp(ceil(q * total), 1, total)
//	walk buckets in ASCENDING index order accumulating counts; the first bucket
//	whose cumulative count >= rank gives the answer; return value(index).
//
// It returns 0 for an empty sketch. This is the Go oracle the integration test
// asserts the SQL against, and it keeps the storage package meaningfully
// covered without a live ClickHouse.
func QuantileFromBuckets(buckets map[int32]uint64, q float64) float64 {
	var total uint64
	for _, c := range buckets {
		total += c
	}
	if total == 0 {
		return 0
	}

	indices := make([]int32, 0, len(buckets))
	for idx := range buckets {
		indices = append(indices, idx)
	}
	sort.Slice(indices, func(i, j int) bool { return indices[i] < indices[j] })

	rank := uint64(math.Ceil(q * float64(total)))
	if rank < 1 {
		rank = 1
	}
	if rank > total {
		rank = total
	}

	var cumulative uint64
	for _, idx := range indices {
		cumulative += buckets[idx]
		if cumulative >= rank {
			return bucketValue(idx)
		}
	}
	// Unreachable: rank <= total means some bucket satisfies it.
	return bucketValue(indices[len(indices)-1])
}
