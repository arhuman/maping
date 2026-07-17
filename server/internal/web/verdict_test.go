package web

import (
	"testing"

	"github.com/arhuman/maping/server/internal/storage"
	"github.com/stretchr/testify/assert"
)

// baselineBuckets returns n trailing baseline buckets each carrying traffic at a
// fixed p95 (seconds), so the median p95 is deterministic.
func baselineBuckets(n int, p95 float64) []storage.TimePoint {
	out := make([]storage.TimePoint, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, storage.TimePoint{Count: 100, P95: p95})
	}
	return out
}

func TestComputeVerdict(t *testing.T) {
	tests := []struct {
		name       string
		d          detailView
		baseline   []storage.TimePoint
		winSeconds float64
		wantLvl    string
		wantDot    string
		wantOpen   bool
		wantQual   string
		// contains is asserted against the Sentence.
		contains []string
	}{
		{
			name:       "healthy",
			d:          detailView{Count: 500, ErrorRate: 0, P50: 0.004, P95: 0.005, P99: 0.01},
			winSeconds: 500,
			wantLvl:    "Healthy",
			wantDot:    "dot-ok",
			contains:   []string{"0.00% errors", "stable latency"},
		},
		{
			// Too few requests to trust the percentiles AND no error signal → Unknown,
			// never a shaky Healthy.
			name:       "low traffic, no errors is Unknown",
			d:          detailView{Count: 12, ErrorRate: 0, P50: 0.01, P95: 0.9},
			winSeconds: 3600,
			wantLvl:    "Unknown",
			wantDot:    "dot-muted",
			contains:   []string{"Insufficient traffic (n=12)"},
		},
		{
			// Errors are a confident fact at any volume: a total failure below the
			// sample gate still grades Critical, tagged "low traffic" for blast radius.
			name:       "low traffic total failure is Critical + low traffic",
			d:          detailView{Count: 12, ErrorRate: 1.0, P50: 0.001, P95: 0.002},
			winSeconds: 3600,
			wantLvl:    "Critical",
			wantDot:    "dot-err",
			wantOpen:   true,
			wantQual:   "low traffic",
			contains:   []string{"100.0% errors"},
		},
		{
			// Regression (the /boom bug): 100% errors on modest traffic past the gate
			// must be Critical, never Healthy — the old count>=100 floor swallowed it.
			name:       "total failure on modest traffic is Critical",
			d:          detailView{Count: 31, ErrorRate: 1.0, P50: 0.00004, P95: 0.0001},
			winSeconds: 86400, // ~24h → negligible rate
			wantLvl:    "Critical",
			wantDot:    "dot-err",
			wantOpen:   true,
			wantQual:   "low traffic",
			contains:   []string{"100.0% errors"},
		},
		{
			name:       "error-driven Degraded",
			d:          detailView{Count: 200, ErrorRate: 0.03, P50: 0.01, P95: 0.012},
			winSeconds: 200, // rate 1/s → no low-traffic qualifier
			wantLvl:    "Degraded",
			wantDot:    "dot-warn",
			wantOpen:   true,
			contains:   []string{"3.00% errors"},
		},
		{
			name:       "error-driven Critical",
			d:          detailView{Count: 200, ErrorRate: 0.08, P50: 0.01, P95: 0.012},
			winSeconds: 200,
			wantLvl:    "Critical",
			wantDot:    "dot-err",
			wantOpen:   true,
			contains:   []string{"8.00% errors"},
		},
		{
			name:       "spread-driven Degraded",
			d:          detailView{Count: 100, ErrorRate: 0, P50: 0.05, P95: 0.15},
			winSeconds: 100,
			wantLvl:    "Degraded",
			wantDot:    "dot-warn",
			wantOpen:   true,
			contains:   []string{"spread 3.0×", "p95 150 ms"},
		},
		{
			name:       "latency-vs-baseline Degraded",
			d:          detailView{Count: 200, ErrorRate: 0, P50: 0.15, P95: 0.3},
			baseline:   baselineBuckets(40, 0.1),
			winSeconds: 200,
			wantLvl:    "Degraded",
			wantDot:    "dot-warn",
			wantOpen:   true,
			contains:   []string{"3.0× baseline"},
		},
		{
			name:       "latency-vs-baseline Critical",
			d:          detailView{Count: 200, ErrorRate: 0, P50: 0.5, P95: 0.9},
			baseline:   baselineBuckets(40, 0.1),
			winSeconds: 200,
			wantLvl:    "Critical",
			wantDot:    "dot-err",
			wantOpen:   true,
			contains:   []string{"9.0× baseline", "p95 900 ms"},
		},
		{
			name:       "baseline unavailable skips latency rule",
			d:          detailView{Count: 200, ErrorRate: 0, P50: 0.15, P95: 0.3},
			baseline:   baselineBuckets(29, 0.1), // one short of the 30-bucket floor
			winSeconds: 200,
			wantLvl:    "Healthy",
			wantDot:    "dot-ok",
			contains:   []string{"steady latency"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := computeVerdict(tc.d, tc.baseline, tc.winSeconds)
			assert.Equal(t, tc.wantLvl, v.Level)
			assert.Equal(t, tc.wantLvl, v.Headline)
			assert.Equal(t, tc.wantDot, v.DotClass)
			assert.Equal(t, tc.wantOpen, v.Open)
			assert.Equal(t, tc.wantQual, v.Qualifier)
			for _, want := range tc.contains {
				assert.Contains(t, v.Sentence, want)
			}
		})
	}
}
