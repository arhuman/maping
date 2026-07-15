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
