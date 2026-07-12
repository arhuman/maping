package sketch

import (
	"math"
	"math/rand"
	"sort"
	"testing"
	"testing/quick"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// relErrBound is the accuracy gate. The Gamma=1.01 guarantee is ~0.4975%; we
// assert <=1% for headroom. Do not weaken this: a failure means the mapping
// math is wrong, not that the bound is too tight.
const relErrBound = 0.01

// trueQuantile computes the exact quantile of a sample slice by sorting, using
// the SAME rank convention as DDSketch.Quantile (rank = ceil(q*n), 1-based).
func trueQuantile(samples []float64, q float64) float64 {
	n := len(samples)
	if n == 0 {
		return 0
	}
	sorted := make([]float64, n)
	copy(sorted, samples)
	sort.Float64s(sorted)
	rank := min(max(int(math.Ceil(q*float64(n))), 1), n)
	return sorted[rank-1]
}

// relErr is the relative error of got vs want. For want==0 it returns the
// absolute error so a divide-by-zero cannot mask a miss.
func relErr(got, want float64) float64 {
	if want == 0 {
		return math.Abs(got)
	}
	return math.Abs(got-want) / math.Abs(want)
}

var testQuantiles = []float64{0.5, 0.9, 0.95, 0.99, 0.999}

// genConstant returns n copies of a single latency.
func genConstant(n int, v float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

// genUniform draws n samples log-uniformly across [MinValue, MaxValue] so the
// whole domain is exercised, not just the seconds range.
func genUniform(r *rand.Rand, n int) []float64 {
	lo, hi := math.Log(MinValue), math.Log(MaxValue)
	out := make([]float64, n)
	for i := range out {
		out[i] = math.Exp(lo + r.Float64()*(hi-lo))
	}
	return out
}

// genLognormal models realistic latency: median ~50ms, heavy right tail,
// clamped into the domain.
func genLognormal(r *rand.Rand, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		v := math.Exp(-3.0 + 1.2*r.NormFloat64())
		out[i] = clamp(v)
	}
	return out
}

// genBimodal mixes a fast cluster (~2ms) and a slow cluster (~5s).
func genBimodal(r *rand.Rand, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		if r.Float64() < 0.7 {
			out[i] = clamp(math.Exp(-6.2 + 0.3*r.NormFloat64()))
		} else {
			out[i] = clamp(math.Exp(1.6 + 0.3*r.NormFloat64()))
		}
	}
	return out
}

func datasets(r *rand.Rand, n int) map[string][]float64 {
	return map[string][]float64{
		"constant_1ms":  genConstant(n, 0.001),
		"constant_10s":  genConstant(n, 10.0),
		"uniform_range": genUniform(r, n),
		"lognormal":     genLognormal(r, n),
		"bimodal":       genBimodal(r, n),
	}
}

func TestComputedDomain(t *testing.T) {
	// Guards the frozen wire contract: these must not drift silently.
	require.Equal(t, -1388, minIndex)
	require.Equal(t, 412, maxIndex)
	require.Equal(t, 1801, maxIndex-minIndex+1)
	require.LessOrEqual(t, maxIndex-minIndex+1, numBuckets)
	require.InDelta(t, 0.004975, (Gamma-1)/(Gamma+1), 1e-6)
}

func TestAccuracyAcrossDistributions(t *testing.T) {
	const n = 100_000
	r := rand.New(rand.NewSource(1))
	for name, samples := range datasets(r, n) {
		t.Run(name, func(t *testing.T) {
			s := New()
			for _, v := range samples {
				s.Add(v)
			}
			require.Equal(t, uint64(n), s.Count())
			for _, q := range testQuantiles {
				want := trueQuantile(samples, q)
				got := s.Quantile(q)
				err := relErr(got, want)
				assert.LessOrEqualf(t, err, relErrBound,
					"q=%.3f true=%.6g got=%.6g relErr=%.4f%%", q, want, got, err*100)
			}
		})
	}
}

func TestMergeMatchesSingleSketch(t *testing.T) {
	const n = 90_000
	r := rand.New(rand.NewSource(2))
	for name, samples := range datasets(r, n) {
		t.Run(name, func(t *testing.T) {
			single := New()
			for _, v := range samples {
				single.Add(v)
			}

			// Split across 3 sketches, then merge into the first.
			parts := []*DDSketch{New(), New(), New()}
			for i, v := range samples {
				parts[i%3].Add(v)
			}
			merged := New()
			for _, p := range parts {
				merged.Merge(p)
			}

			// Merge is exact: bucket arrays and count must be identical.
			require.Equal(t, single.count, merged.count)
			require.Equal(t, single.buckets, merged.buckets)

			// And the merged sketch still meets the accuracy bound.
			for _, q := range testQuantiles {
				want := trueQuantile(samples, q)
				got := merged.Quantile(q)
				err := relErr(got, want)
				assert.LessOrEqualf(t, err, relErrBound,
					"q=%.3f true=%.6g got=%.6g relErr=%.4f%%", q, want, got, err*100)
			}
		})
	}
}

func TestClamping(t *testing.T) {
	s := New()

	// Below MinValue and pathological inputs land in the low edge bucket.
	require.NotPanics(t, func() {
		s.Add(-5)
		s.Add(0)
		s.Add(1e-30)
		s.Add(math.NaN())
		s.Add(math.Inf(-1))
	})
	lowIdx := index(MinValue)
	require.Equal(t, uint64(5), s.buckets[lowIdx-minIndex])

	// Above MaxValue (including +Inf) lands in the high edge bucket.
	require.NotPanics(t, func() {
		s.Add(1e6)
		s.Add(math.Inf(1))
	})
	highIdx := index(MaxValue)
	require.Equal(t, uint64(2), s.buckets[highIdx-minIndex])

	require.Equal(t, uint64(7), s.Count())

	// Out-of-range quantiles clamp sensibly to the first/last populated bucket.
	require.InEpsilon(t, value(lowIdx), s.Quantile(-1), relErrBound)
	require.InEpsilon(t, value(highIdx), s.Quantile(2), relErrBound)
}

func TestBucketsRoundTrip(t *testing.T) {
	const n = 50_000
	r := rand.New(rand.NewSource(3))
	for name, samples := range datasets(r, n) {
		t.Run(name, func(t *testing.T) {
			s := New()
			for _, v := range samples {
				s.Add(v)
			}
			rebuilt := FromBuckets(s.Buckets())

			require.Equal(t, s.count, rebuilt.count)
			require.Equal(t, s.buckets, rebuilt.buckets)
			for _, q := range testQuantiles {
				require.Equal(t, s.Quantile(q), rebuilt.Quantile(q))
			}
		})
	}
}

func TestBucketsOmitsZeros(t *testing.T) {
	s := New()
	s.Add(0.001)
	s.Add(0.001)
	s.Add(5.0)
	m := s.Buckets()
	require.Len(t, m, 2)
	for _, c := range m {
		require.NotZero(t, c)
	}
}

func TestQuantileFromBucketsAgreesWithSketch(t *testing.T) {
	const n = 80_000
	r := rand.New(rand.NewSource(4))
	for name, samples := range datasets(r, n) {
		t.Run(name, func(t *testing.T) {
			s := New()
			for _, v := range samples {
				s.Add(v)
			}
			m := s.Buckets()
			for _, q := range testQuantiles {
				require.Equalf(t, s.Quantile(q), QuantileFromBuckets(m, q),
					"disagreement at q=%.3f", q)
			}
		})
	}
}

func TestQuantileFromBucketsEmpty(t *testing.T) {
	require.Equal(t, 0.0, QuantileFromBuckets(nil, 0.5))
	require.Equal(t, 0.0, QuantileFromBuckets(map[int32]uint64{}, 0.99))
}

func TestEmptySketch(t *testing.T) {
	s := New()
	require.Equal(t, uint64(0), s.Count())
	for _, q := range append(testQuantiles, 0, 1) {
		require.Equal(t, 0.0, s.Quantile(q))
	}
}

// TestMergeAssociative checks that merge order does not change the result,
// which is the property rollups depend on.
func TestMergeAssociative(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	a, b, c := New(), New(), New()
	for range 10_000 {
		a.Add(genLognormal(r, 1)[0])
		b.Add(genLognormal(r, 1)[0])
		c.Add(genLognormal(r, 1)[0])
	}

	// (a+b)+c
	left := New()
	left.Merge(a)
	left.Merge(b)
	left.Merge(c)

	// a+(b+c)
	bc := New()
	bc.Merge(b)
	bc.Merge(c)
	right := New()
	right.Merge(a)
	right.Merge(bc)

	require.Equal(t, left.buckets, right.buckets)
	require.Equal(t, left.count, right.count)
}

// TestQuantilePropertyMonotonic uses quick to assert quantiles are
// non-decreasing in q for arbitrary sketches, an invariant that catches
// off-by-one rank bugs.
func TestQuantilePropertyMonotonic(t *testing.T) {
	f := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))
		s := New()
		for range 1000 {
			s.Add(genLognormal(r, 1)[0])
		}
		prev := s.Quantile(0)
		for _, q := range []float64{0.1, 0.25, 0.5, 0.75, 0.9, 0.99, 1.0} {
			cur := s.Quantile(q)
			if cur < prev {
				return false
			}
			prev = cur
		}
		return true
	}
	require.NoError(t, quick.Check(f, &quick.Config{MaxCount: 50}))
}

// BenchmarkAdd verifies the hot path is allocation-free. The fixed-array store
// must not allocate on Add; assert 0 allocs/op when run with -benchmem.
func BenchmarkAdd(b *testing.B) {
	s := New()
	r := rand.New(rand.NewSource(7))
	values := make([]float64, 4096)
	for i := range values {
		values[i] = genLognormal(r, 1)[0]
	}
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		s.Add(values[i&4095])
		i++
	}
}
