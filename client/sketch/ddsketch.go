// Package sketch implements a hand-rolled DDSketch specialised for request
// latency aggregation, as decided in ADR-0001.
//
// DDSketch is a quantile sketch with a provable relative-error bound and an
// exact, associative merge. Both properties are load-bearing for mAPI-ng: merge
// lets latency roll up (instances into a service, 10s windows into minutes into
// hours) without accumulating error, and the relative-error bound makes
// percentile estimates accurate across services whose latencies span
// microseconds to tens of seconds with zero tuning.
//
// # Mapping
//
// This implementation uses the standard logarithmic mapping with a frozen
// growth factor Gamma = 1.01. A latency v (in seconds) maps to bucket index
//
//	index(v) = ceil(log(v) / log(Gamma))
//
// and a bucket index i is estimated back to the value
//
//	value(i) = 2 * Gamma^i / (Gamma + 1)
//
// which is the geometric midpoint of the bucket. The guaranteed relative
// accuracy is (Gamma-1)/(Gamma+1) (~0.4975% for Gamma=1.01): the value returned
// for a quantile is within that relative error of the true value at that rank.
//
// # Storage
//
// The value domain is clamped to [MinValue, MaxValue] = [1e-6, 60.0] seconds.
// Buckets are held in a fixed-size array (no map) so Add is allocation-free on
// the hot path. Index i is stored at buckets[i-minIndex]. Values outside the
// domain are clamped to the domain before mapping, so they always land in the
// edge buckets and never index out of bounds.
//
// # Wire format
//
// Buckets and FromBuckets convert to and from a sparse map keyed by the
// absolute bucket index, matching the proto map<int32,uint64> on the wire.
// SketchFormatVersion identifies this bucket layout; Gamma and the value domain
// are a wire contract and must not change without a format migration.
package sketch

import "math"

// Gamma is the frozen bucket-growth factor of the logarithmic mapping. It is a
// wire contract: changing it changes every bucket index and is a breaking
// format migration.
const Gamma = 1.01

// SketchFormatVersion identifies this bucket layout on the wire. Bump it only
// alongside a format migration (a change to Gamma or the value domain).
const SketchFormatVersion uint32 = 1

// MinValue and MaxValue bound the latency domain in seconds (1µs..60s). Values
// outside this range are clamped before mapping.
const (
	MinValue = 1e-6
	MaxValue = 60.0
)

// numBuckets is the fixed backing-array size. The [MinValue, MaxValue] domain
// spans 1801 indices for Gamma=1.01; 2048 leaves comfortable margin. The init
// assertion below guarantees the span can never overflow this array.
const numBuckets = 2048

// logGamma is math.Log(Gamma), precomputed to keep index() division-only.
var logGamma = math.Log(Gamma)

// minIndex and maxIndex are the bucket indices of the clamped domain bounds.
// buckets[i-minIndex] holds the count for absolute index i.
var (
	minIndex = index(MinValue)
	maxIndex = index(MaxValue)
)

func init() {
	if maxIndex-minIndex+1 > numBuckets {
		panic("sketch: numBuckets too small for [MinValue, MaxValue] domain")
	}
}

// index returns the DDSketch bucket index for a strictly positive value.
func index(v float64) int {
	return int(math.Ceil(math.Log(v) / logGamma))
}

// value returns the estimated value (bucket midpoint) for a bucket index.
func value(i int) float64 {
	return 2 * math.Pow(Gamma, float64(i)) / (Gamma + 1)
}

// clamp restricts a latency to the [MinValue, MaxValue] domain, folding NaN and
// non-positive values to MinValue so they land in the low edge bucket.
func clamp(seconds float64) float64 {
	if math.IsNaN(seconds) || seconds < MinValue {
		return MinValue
	}
	if seconds > MaxValue {
		return MaxValue
	}
	return seconds
}

// DDSketch is a fixed-array DDSketch over the latency domain. The zero value is
// not usable; construct one with New.
type DDSketch struct {
	buckets [numBuckets]uint64
	count   uint64
}

// New returns an empty DDSketch.
func New() *DDSketch {
	return &DDSketch{}
}

// Add records one latency observation in seconds. Values are clamped to
// [MinValue, MaxValue]; NaN, Inf, and non-positive values are folded to
// MinValue. Add performs no allocation.
func (s *DDSketch) Add(seconds float64) {
	i := index(clamp(seconds))
	s.buckets[i-minIndex]++
	s.count++
}

// Merge adds another sketch into this one, bucket by bucket. The operation is
// exact and associative: merging is equivalent to feeding all observations to a
// single sketch.
func (s *DDSketch) Merge(o *DDSketch) {
	for i := range s.buckets {
		s.buckets[i] += o.buckets[i]
	}
	s.count += o.count
}

// Count returns the number of observations recorded.
func (s *DDSketch) Count() uint64 {
	return s.count
}

// Quantile returns the estimated value in seconds for quantile q in [0, 1]. It
// returns 0 for an empty sketch. q is clamped to [0, 1].
//
// The rank convention is rank = ceil(q * count) clamped to [1, count]: the
// returned bucket is the smallest whose cumulative count reaches that rank.
// QuantileFromBuckets uses the same convention and agrees exactly.
func (s *DDSketch) Quantile(q float64) float64 {
	if s.count == 0 {
		return 0
	}
	rank := quantileRank(q, s.count)
	var cum uint64
	for i := range s.buckets {
		cum += s.buckets[i]
		if cum >= rank {
			return value(i + minIndex)
		}
	}
	// Unreachable while count > 0: the cumulative sum reaches count >= rank.
	return value(maxIndex)
}

// Buckets returns the non-zero buckets as a sparse map keyed by absolute bucket
// index, ready for the wire (proto map<int32,uint64>). Zero buckets are omitted.
func (s *DDSketch) Buckets() map[int32]uint64 {
	m := make(map[int32]uint64)
	for i := range s.buckets {
		if c := s.buckets[i]; c != 0 {
			m[int32(i+minIndex)] = c
		}
	}
	return m
}

// FromBuckets reconstructs a sketch from a sparse absolute-index map, the
// inverse of Buckets. Indices outside the domain are clamped into the edge
// buckets so reconstruction never panics.
func FromBuckets(m map[int32]uint64) *DDSketch {
	s := New()
	for absIdx, c := range m {
		i := int(absIdx)
		if i < minIndex {
			i = minIndex
		} else if i > maxIndex {
			i = maxIndex
		}
		s.buckets[i-minIndex] += c
		s.count += c
	}
	return s
}

// QuantileFromBuckets estimates quantile q in [0, 1] directly from a sparse
// absolute-index bucket map, mirroring the ClickHouse percentile query so
// client and server share one definition. It uses the same rank convention as
// DDSketch.Quantile (rank = ceil(q * total), clamped to [1, total]) and agrees
// exactly on the same data. It returns 0 for an empty map.
func QuantileFromBuckets(m map[int32]uint64, q float64) float64 {
	var total uint64
	for _, c := range m {
		total += c
	}
	if total == 0 {
		return 0
	}
	rank := quantileRank(q, total)

	// Walk buckets in ascending index order without allocating a sorted slice
	// of the sparse keys: scan the dense domain, hitting only present indices.
	var cum uint64
	for i := minIndex; i <= maxIndex; i++ {
		c, ok := m[int32(i)]
		if !ok {
			continue
		}
		cum += c
		if cum >= rank {
			return value(i)
		}
	}
	return value(maxIndex)
}

// quantileRank converts a quantile in [0, 1] to a 1-based rank threshold,
// clamped to [1, total]. rank = ceil(q * total), with q=0 yielding rank 1.
func quantileRank(q float64, total uint64) uint64 {
	if q < 0 {
		q = 0
	} else if q > 1 {
		q = 1
	}
	rank := max(uint64(math.Ceil(q*float64(total))), 1)
	return min(rank, total)
}
