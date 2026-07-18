package web

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arhuman/maping/server/internal/storage"
)

// diagnosis tests use a fixed window so the fleet rate math (cores, alloc/s,
// gc/s) is controlled purely by the counter totals set per case.
const (
	diagWinSec  = 300.0
	diagBaseSec = 300.0
)

// baseParams returns a Degraded endpoint with every signal empty, so each case
// overrides only the fields for the cause it exercises. A Degraded verdict passes
// the anomaly gate without asserting any particular cause.
func baseParams() diagnosisParams {
	return diagnosisParams{
		Detail:          detailView{Count: 1000, ErrorRate: 0.1, P50: 0.05, P95: 0.4},
		Verdict:         verdictView{Level: "Degraded"},
		WinSeconds:      diagWinSec,
		BaselineSeconds: diagBaseSec,
	}
}

// coresNs returns the cpu_ns a single instance must report for the fleet to read
// `cores` cores over the test window.
func coresNs(cores float64) uint64 { return uint64(cores * diagWinSec * 1e9) }

// leakMemory builds a memoryVerdict that reads as a clear leak, via the real rule.
func leakMemory() memoryVerdict {
	return computeMemoryVerdict(memSeries(100*mib, 110*mib, 260*mib, 280*mib))
}

// TestComputeDiagnosisCauses exercises each cause in isolation and asserts the
// ranked TopCause.Name and Show. The TopCause.Name strings are the diagnosis
// contract the user owns; flagged for validation. Evidence wording is asserted
// only NotEmpty.
func TestComputeDiagnosisCauses(t *testing.T) {
	tests := []struct {
		name     string
		params   diagnosisParams
		wantShow bool
		wantTop  string
	}{
		{
			name: "memory leak surfaces as Memory cause",
			params: func() diagnosisParams {
				p := baseParams()
				p.Verdict = verdictView{Level: "Healthy"} // flat-RED: leak must still surface
				p.Memory = leakMemory()
				return p
			}(),
			wantShow: true,
			wantTop:  "Memory / GC pressure",
		},
		{
			name: "cores near GOMAXPROCS surfaces as CPU saturation",
			params: func() diagnosisParams {
				p := baseParams()
				p.Resources = []storage.InstanceResourceStat{
					{Instance: "i-1", GOMAXPROCS: 4, CPUNs: coresNs(4)},
				}
				return p
			}(),
			wantShow: true,
			wantTop:  "CPU saturation",
		},
		{
			name: "self-time up with CPU/GC flat and in-flight up surfaces as Congestion",
			params: func() diagnosisParams {
				p := baseParams()
				p.Resources = []storage.InstanceResourceStat{
					{Instance: "i-1", GOMAXPROCS: 4, CPUNs: coresNs(1), GCCPUFraction: 0.05, InFlight: 10},
				}
				p.ResourceBaseline = []storage.InstanceResourceStat{
					{Instance: "i-1", GOMAXPROCS: 4, CPUNs: coresNs(1), GCCPUFraction: 0.05, InFlight: 2},
				}
				// Self-dominant: downstream is only 10% of request time.
				p.Downstream = storage.DownstreamStat{Count: 1, SumDurationNs: 100, SumDownstreamNs: 10}
				return p
			}(),
			wantShow: true,
			wantTop:  "Connection / pool congestion",
		},
		{
			name: "downstream-dominated time surfaces as Downstream",
			params: func() diagnosisParams {
				p := baseParams()
				p.Downstream = storage.DownstreamStat{Count: 1, SumDurationNs: 100, SumDownstreamNs: 80}
				return p
			}(),
			wantShow: true,
			wantTop:  "Downstream / IO",
		},
		{
			name: "deadline aborts surface as Overload",
			params: func() diagnosisParams {
				p := baseParams()
				p.NoStatus = []storage.NoStatusReasonStat{{Reason: 2, Count: 100}} // deadline
				return p
			}(),
			wantShow: true,
			wantTop:  "Overload / timeouts",
		},
		{
			name: "goroutine growth surfaces as Goroutine leak",
			params: func() diagnosisParams {
				p := baseParams()
				p.Resources = []storage.InstanceResourceStat{{Instance: "i-1", Goroutines: 1000}}
				p.ResourceBaseline = []storage.InstanceResourceStat{{Instance: "i-1", Goroutines: 100}}
				return p
			}(),
			wantShow: true,
			wantTop:  "Goroutine leak",
		},
		{
			name: "p95 outlier instance surfaces as Instance-localized",
			params: func() diagnosisParams {
				p := baseParams()
				p.Instances = []instanceStatRow{
					{Instance: "i-1", Count: 500, P95: 0.05},
					{Instance: "i-2", Count: 500, P95: 0.5, IsOutlier: true},
				}
				return p
			}(),
			wantShow: true,
			wantTop:  "Instance-localized",
		},
		{
			name: "worse version p95 surfaces as Release regression",
			params: func() diagnosisParams {
				p := baseParams()
				p.Versions = []storage.VersionStat{
					{Version: "v1.0.0", Count: 500, P95: 0.1},
					{Version: "v1.1.0", Count: 500, P95: 0.4},
				}
				return p
			}(),
			wantShow: true,
			wantTop:  "Release regression",
		},
		{
			name: "healthy endpoint with stable memory shows no card",
			params: func() diagnosisParams {
				p := baseParams()
				p.Verdict = verdictView{Level: "Healthy"}
				p.Memory = memoryVerdict{Level: "Stable"}
				return p
			}(),
			wantShow: false,
		},
		{
			name: "anomalous with no signal is Unattributed",
			params: func() diagnosisParams {
				return baseParams() // Degraded, everything empty
			}(),
			wantShow: true,
			wantTop:  "Unattributed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := computeDiagnosis(tc.params)
			assert.Equal(t, tc.wantShow, d.Show, "Show")
			if !tc.wantShow {
				return
			}
			assert.Equal(t, tc.wantTop, d.TopCause.Name, "TopCause.Name")
			assert.NotEmpty(t, d.TopCause.Evidence, "TopCause.Evidence")
			assert.NotEmpty(t, d.TopCause.Falsifier, "TopCause.Falsifier")
			assert.NotEmpty(t, d.Why, "Why")
		})
	}
}

// TestComputeDiagnosisRanking confirms a more-corroborated cause (Memory with
// three resource signals) outranks a single-signal cause (Instance-localized),
// which then appears in Others. Signal count dominates the ranking.
func TestComputeDiagnosisRanking(t *testing.T) {
	p := baseParams()
	p.Memory = leakMemory() // leak signal
	p.Resources = []storage.InstanceResourceStat{
		{Instance: "i-1", GOMAXPROCS: 4, CPUNs: coresNs(0.5), GCCPUFraction: 0.30, TotalAllocBytes: 1_000_000_000, Goroutines: 50},
	}
	p.ResourceBaseline = []storage.InstanceResourceStat{
		{Instance: "i-1", GOMAXPROCS: 4, CPUNs: coresNs(0.5), GCCPUFraction: 0.05, TotalAllocBytes: 100_000_000, Goroutines: 50},
	}
	p.Instances = []instanceStatRow{
		{Instance: "i-1", Count: 500, P95: 0.05},
		{Instance: "i-2", Count: 500, P95: 0.5, IsOutlier: true},
	}

	d := computeDiagnosis(p)
	require.True(t, d.Show)
	assert.Equal(t, "Memory / GC pressure", d.TopCause.Name, "TopCause.Name")
	require.NotEmpty(t, d.Others, "Others")
	names := make([]string, 0, len(d.Others))
	for _, o := range d.Others {
		names = append(names, o.Name)
	}
	assert.Contains(t, names, "Instance-localized")
}
