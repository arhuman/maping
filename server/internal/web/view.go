package web

import (
	"sort"
	"time"

	"github.com/arhuman/maping/server/internal/storage"
)

// This file holds the pure presentation mappers: storage stats -> display rows
// (rate = count/window, error% color threshold, formatted percentiles), the
// server-side endpoint-table sort, and the 4-step onboarding builder. Keeping
// them free of I/O makes every derivation unit-testable without HTTP or
// ClickHouse.

// errorRateWarn is the fraction above which an error% cell is flagged "high" so
// the template can color it. 5% is a pragmatic RED-dashboard threshold, not a
// per-tenant SLA (v1 has no alerting; CONTEXT defers it to v2).
const errorRateWarn = 0.05

// serviceRow is one rendered row of the service-overview table.
type serviceRow struct {
	Service    string
	Count      uint64
	RatePerSec float64
	ErrorRate  float64
	ErrorHigh  bool
	P50        float64
	P95        float64
	P99        float64
	// DrillHref is the overview→endpoints link. It carries ?sort=error when the
	// service is unhealthy (warn/err) so the operator lands already triaged on
	// the worst endpoints; a healthy service drills to the default traffic sort.
	DrillHref string
}

// endpointRow is one rendered row of the endpoint table. ReqBytesAvg /
// RespBytesAvg are the per-request average payload sizes (bytes) for the
// bytes-symmetry columns.
type endpointRow struct {
	Method       string
	Route        string
	Count        uint64
	RatePerSec   float64
	ErrorRate    float64
	ErrorHigh    bool
	P50          float64
	P95          float64
	P99          float64
	ReqBytesAvg  float64
	RespBytesAvg float64
}

// ratePerSec derives the request rate from an aggregate count over the window.
// It is count/window-seconds; window is the fixed dashboard lookback.
func ratePerSec(count uint64, w time.Duration) float64 {
	secs := w.Seconds()
	if secs <= 0 {
		return 0
	}
	return float64(count) / secs
}

// toServiceRows maps storage service stats into display rows. winKey is threaded
// onto each drill href so opening a service keeps the selected lookback.
func toServiceRows(stats []storage.ServiceStat, w time.Duration, winKey string) []serviceRow {
	out := make([]serviceRow, 0, len(stats))
	for _, s := range stats {
		href := "/services/" + s.Service
		// Unhealthy (warn/err) services drill straight into the error-sorted
		// endpoint table — the triage order — instead of the traffic default.
		if healthClass(s.ErrorRate) != "dot-ok" {
			href += "?sort=" + sortError
		}
		href = withWin(href, winKey)
		out = append(out, serviceRow{
			Service:    s.Service,
			Count:      s.Count,
			RatePerSec: ratePerSec(s.Count, w),
			ErrorRate:  s.ErrorRate,
			ErrorHigh:  s.ErrorRate >= errorRateWarn,
			P50:        s.P50,
			P95:        s.P95,
			P99:        s.P99,
			DrillHref:  href,
		})
	}
	return out
}

// toEndpointRows maps storage endpoint stats into display rows.
func toEndpointRows(stats []storage.EndpointStat, w time.Duration) []endpointRow {
	out := make([]endpointRow, 0, len(stats))
	for _, e := range stats {
		out = append(out, endpointRow{
			Method:       e.Method,
			Route:        e.Route,
			Count:        e.Count,
			RatePerSec:   ratePerSec(e.Count, w),
			ErrorRate:    e.ErrorRate,
			ErrorHigh:    e.ErrorRate >= errorRateWarn,
			P50:          e.P50,
			P95:          e.P95,
			P99:          e.P99,
			ReqBytesAvg:  e.ReqBytesAvg,
			RespBytesAvg: e.RespBytesAvg,
		})
	}
	return out
}

// sortTraffic/sortError/sortP99 are the allowlisted server-side sort keys for
// the endpoint table. An unknown ?sort= value falls back to traffic (the
// default), so a crafted query param can never reach an unvetted column.
const (
	sortTraffic = "traffic"
	sortError   = "error"
	sortP99     = "p99"
)

// normalizeSort maps a raw ?sort= param to an allowlisted key, defaulting to
// traffic. This is the guard: only these three keys ever drive the sort.
func normalizeSort(raw string) string {
	switch raw {
	case sortError:
		return sortError
	case sortP99:
		return sortP99
	default:
		return sortTraffic
	}
}

// sortEndpointRows sorts rows in place by the allowlisted key, all descending
// (highest traffic / worst error rate / slowest p99 first — the operator's
// triage order). Ties fall back to route for a stable, deterministic display.
func sortEndpointRows(rows []endpointRow, key string) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		switch key {
		case sortError:
			if a.ErrorRate != b.ErrorRate {
				return a.ErrorRate > b.ErrorRate
			}
		case sortP99:
			if a.P99 != b.P99 {
				return a.P99 > b.P99
			}
		default: // sortTraffic
			if a.Count != b.Count {
				return a.Count > b.Count
			}
		}
		return a.Route < b.Route
	})
}
