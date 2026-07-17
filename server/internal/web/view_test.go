package web

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arhuman/maping/server/internal/storage"
)

func TestRatePerSec(t *testing.T) {
	// 3600 requests over a 1h window = 1 req/s.
	assert.InDelta(t, 1.0, ratePerSec(3600, time.Hour), 1e-9)
	// Zero window is safe.
	assert.Equal(t, 0.0, ratePerSec(10, 0))
}

func TestToServiceRowsErrorThreshold(t *testing.T) {
	rows := toServiceRows([]storage.ServiceStat{
		{Service: "a", Count: 100, ErrorRate: 0.049},
		{Service: "b", Count: 100, ErrorRate: 0.05},
	}, time.Hour, "1h")
	require.Len(t, rows, 2)
	assert.False(t, rows[0].ErrorHigh, "just below threshold is not high")
	assert.True(t, rows[1].ErrorHigh, "at threshold is high")
}

func TestNormalizeSort(t *testing.T) {
	cases := map[string]string{
		"traffic": sortTraffic,
		"error":   sortError,
		"p99":     sortP99,
		"":        sortTraffic,
		"garbage": sortTraffic,
		";DROP":   sortTraffic,
	}
	for in, want := range cases {
		assert.Equal(t, want, normalizeSort(in), "sort=%q", in)
	}
}

func TestSortEndpointRows(t *testing.T) {
	base := func() []endpointRow {
		return []endpointRow{
			{Route: "/a", Count: 10, ErrorRate: 0.02, P99: 0.9},
			{Route: "/b", Count: 100, ErrorRate: 0.50, P99: 0.1},
			{Route: "/c", Count: 50, ErrorRate: 0.10, P99: 0.5},
		}
	}

	t.Run("traffic desc", func(t *testing.T) {
		rows := base()
		sortEndpointRows(rows, sortTraffic)
		assert.Equal(t, []string{"/b", "/c", "/a"}, routes(rows))
	})
	t.Run("error desc", func(t *testing.T) {
		rows := base()
		sortEndpointRows(rows, sortError)
		assert.Equal(t, []string{"/b", "/c", "/a"}, routes(rows))
	})
	t.Run("p99 desc", func(t *testing.T) {
		rows := base()
		sortEndpointRows(rows, sortP99)
		assert.Equal(t, []string{"/a", "/c", "/b"}, routes(rows))
	})
	t.Run("tie breaks on route", func(t *testing.T) {
		rows := []endpointRow{
			{Route: "/z", Count: 5}, {Route: "/a", Count: 5},
		}
		sortEndpointRows(rows, sortTraffic)
		assert.Equal(t, []string{"/a", "/z"}, routes(rows))
	})
}

func routes(rows []endpointRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Route
	}
	return out
}

func TestToDetailViewErrorClasses(t *testing.T) {
	v := toDetailView(storage.EndpointDetail{
		Count: 100, ErrorRate: 0.20,
		StatusClasses: []storage.StatusClassCount{
			{Class: "2xx", Count: 80}, {Class: "4xx", Count: 15},
			{Class: "5xx", Count: 5}, {Class: "no_status", Count: 0},
		},
		StatusCodes: map[uint32]uint64{500: 5, 200: 80, 404: 15},
	})
	assert.True(t, v.ErrorHigh)
	// 2xx is not an error; 4xx/5xx/no_status are.
	byClass := map[string]bool{}
	for _, c := range v.Classes {
		byClass[c.Class] = c.IsError
	}
	assert.False(t, byClass["2xx"])
	assert.True(t, byClass["4xx"])
	assert.True(t, byClass["5xx"])
	assert.True(t, byClass["no_status"])

	// Codes sorted ascending.
	require.Len(t, v.Codes, 3)
	assert.Equal(t, uint32(200), v.Codes[0].Code)
	assert.Equal(t, uint32(500), v.Codes[2].Code)
}

func TestBuildOnboarding(t *testing.T) {
	t.Run("no source connected", func(t *testing.T) {
		o := buildOnboarding(nil, false)
		require.Len(t, o.Steps, 4)
		assert.True(t, o.Steps[0].Done, "key valid always done")
		assert.False(t, o.Steps[1].Done, "no service connected")
		assert.False(t, o.Steps[2].Done)
		assert.False(t, o.Steps[3].Done)
		assert.False(t, o.Frozen)
	})
	t.Run("service connected, awaiting data", func(t *testing.T) {
		o := buildOnboarding([]ServiceOnboarding{{Service: "s", Instance: "i"}}, true)
		assert.True(t, o.Steps[1].Done, "service connected")
		assert.True(t, o.Steps[2].Done, "waiting for first summary")
		assert.False(t, o.Steps[3].Done, "first data not yet received")
		assert.True(t, o.Frozen)
		require.Len(t, o.Connected, 1)
	})
}

func TestBuildPerformance(t *testing.T) {
	shell := Shell{}

	t.Run("no data yet", func(t *testing.T) {
		p := buildPerformance(shell, storage.PerformanceStat{}, 24*time.Hour, 2*time.Millisecond, "24h")
		assert.False(t, p.HasData)
		assert.Equal(t, "2", p.QueryMs, "query latency is shown even with no data")
	})

	t.Run("real traffic compresses and saves disk", func(t *testing.T) {
		// 4.4M requests carried by 1000 summaries at ~400 B/summary on disk.
		stat := storage.PerformanceStat{Requests: 4_400_000, Summaries: 1000, SummaryDiskBytes: 400_000}
		p := buildPerformance(shell, stat, 24*time.Hour, 5*time.Millisecond, "24h")
		require.True(t, p.HasData)
		assert.Equal(t, "4.4M", p.Requests)
		assert.Equal(t, "1k", p.Summaries)
		assert.Equal(t, "4.4k×", p.Compression, "requests per shipped summary")
		// Raw pipeline projected at bytesPerRawEvent/event dwarfs the measured summaries.
		assert.NotEqual(t, "—", p.Ratio)
		assert.NotEmpty(t, p.RawDisk)
		assert.NotEmpty(t, p.SummaryDisk)
	})

	t.Run("very low traffic claims no reduction", func(t *testing.T) {
		// A handful of requests but each summary carries fixed sketch overhead, so
		// the projected raw size does not exceed the measured summary size.
		stat := storage.PerformanceStat{Requests: 3, Summaries: 2, SummaryDiskBytes: 8000}
		p := buildPerformance(shell, stat, 24*time.Hour, time.Millisecond, "24h")
		require.True(t, p.HasData)
		assert.Equal(t, "—", p.Ratio, "no honest reduction to claim at trivial volume")
		assert.Equal(t, "100%", p.SummaryBarPct)
	})
}

// TestToResourceRowsIntensities asserts the raw per-window CPU and GC-pause deltas
// are converted into intensities: average cores consumed and STW GC-pause share of
// wall time, with the window guard and the GC-share clamp.
func TestToResourceRowsIntensities(t *testing.T) {
	const winSec = 100.0 // 100s window -> 1e11 ns

	t.Run("cores and gc share", func(t *testing.T) {
		// 87s of CPU over a 100s window -> 0.87 cores; 1.2s STW pause -> 1.2%.
		rows := toResourceRows([]storage.InstanceResourceStat{
			{Instance: "pod-a", CPUNs: 87e9, GCPauseNs: 1.2e9, RSSBytes: 2048, HeapAllocBytes: 1024, Goroutines: 42},
		}, winSec)
		require.Len(t, rows, 1)
		assert.InDelta(t, 0.87, rows[0].CoresUsed, 1e-9)
		assert.InDelta(t, 0.012, rows[0].GCShare, 1e-9)
		// Gauges pass through unchanged.
		assert.Equal(t, 2048.0, rows[0].RSSBytes)
		assert.Equal(t, 1024.0, rows[0].HeapBytes)
		assert.Equal(t, uint64(42), rows[0].Goroutines)
	})

	t.Run("gc share clamps to 1", func(t *testing.T) {
		// Overlapping/merged samples could push summed GC pause past wall time.
		rows := toResourceRows([]storage.InstanceResourceStat{
			{Instance: "pod-a", GCPauseNs: 2 * winSec * 1e9},
		}, winSec)
		require.Len(t, rows, 1)
		assert.Equal(t, 1.0, rows[0].GCShare)
	})

	t.Run("non-positive window yields zero intensities", func(t *testing.T) {
		rows := toResourceRows([]storage.InstanceResourceStat{
			{Instance: "pod-a", CPUNs: 87e9, GCPauseNs: 1.2e9, RSSBytes: 2048},
		}, 0)
		require.Len(t, rows, 1)
		assert.Zero(t, rows[0].CoresUsed)
		assert.Zero(t, rows[0].GCShare)
		// Byte gauges are independent of the window and still map through.
		assert.Equal(t, 2048.0, rows[0].RSSBytes)
	})
}

// TestToInstanceRowsOutlier asserts the p95 outlier flag: it fires only with at
// least two trafficked instances and a p95 both >= 2x the fleet median and above
// the 100ms floor, and never on a tight fleet.
func TestToInstanceRowsOutlier(t *testing.T) {
	t.Run("one replica flagged, fleet clean", func(t *testing.T) {
		// Fleet median p95 = 0.1s; pod-c at 0.9s clears both 2x and the 100ms floor.
		rows := toInstanceRows([]storage.InstanceStat{
			{Instance: "pod-a", Count: 100, P95: 0.1},
			{Instance: "pod-b", Count: 100, P95: 0.1},
			{Instance: "pod-c", Count: 100, P95: 0.9},
		})
		require.Len(t, rows, 3)
		assert.False(t, rows[0].IsOutlier)
		assert.False(t, rows[1].IsOutlier)
		assert.True(t, rows[2].IsOutlier)
	})

	t.Run("floor gates a fast fleet", func(t *testing.T) {
		// pod-c is 5x the median but still under 100ms -> not an outlier.
		rows := toInstanceRows([]storage.InstanceStat{
			{Instance: "pod-a", Count: 100, P95: 0.01},
			{Instance: "pod-b", Count: 100, P95: 0.01},
			{Instance: "pod-c", Count: 100, P95: 0.05},
		})
		for _, r := range rows {
			assert.False(t, r.IsOutlier, "%s under the 100ms floor", r.Instance)
		}
	})

	t.Run("single trafficked instance never flags", func(t *testing.T) {
		// Only one instance has traffic, so there is no fleet to compare against.
		rows := toInstanceRows([]storage.InstanceStat{
			{Instance: "pod-a", Count: 100, P95: 5.0},
			{Instance: "pod-b", Count: 0, P95: 0},
		})
		assert.False(t, rows[0].IsOutlier)
		assert.False(t, rows[1].IsOutlier)
	})

	t.Run("tight fleet has no outlier", func(t *testing.T) {
		rows := toInstanceRows([]storage.InstanceStat{
			{Instance: "pod-a", Count: 100, P95: 0.2},
			{Instance: "pod-b", Count: 100, P95: 0.21},
			{Instance: "pod-c", Count: 100, P95: 0.22},
		})
		for _, r := range rows {
			assert.False(t, r.IsOutlier, "%s within a tight fleet", r.Instance)
		}
	})
}
