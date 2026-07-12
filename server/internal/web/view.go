package web

import (
	"fmt"
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

// endpointRow is one rendered row of the endpoint table.
type endpointRow struct {
	Method     string
	Route      string
	Count      uint64
	RatePerSec float64
	ErrorRate  float64
	ErrorHigh  bool
	P50        float64
	P95        float64
	P99        float64
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

// toServiceRows maps storage service stats into display rows.
func toServiceRows(stats []storage.ServiceStat, w time.Duration) []serviceRow {
	out := make([]serviceRow, 0, len(stats))
	for _, s := range stats {
		href := "/services/" + s.Service
		// Unhealthy (warn/err) services drill straight into the error-sorted
		// endpoint table — the triage order — instead of the traffic default.
		if healthClass(s.ErrorRate) != "dot-ok" {
			href += "?sort=" + sortError
		}
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
			Method:     e.Method,
			Route:      e.Route,
			Count:      e.Count,
			RatePerSec: ratePerSec(e.Count, w),
			ErrorRate:  e.ErrorRate,
			ErrorHigh:  e.ErrorRate >= errorRateWarn,
			P50:        e.P50,
			P95:        e.P95,
			P99:        e.P99,
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

// statusClassView is one class row in the endpoint-detail breakdown.
type statusClassView struct {
	Class   string
	Count   uint64
	IsError bool // 4xx/5xx/no_status count toward the headline error rate.
}

// statusCodeView is one exact-code row, sorted ascending by code for a stable
// display.
type statusCodeView struct {
	Code  uint32
	Count uint64
}

// detailView is the rendered endpoint-detail headline: RED numbers plus the
// class breakdown and exact-code table shown beside the error rate.
type detailView struct {
	Count     uint64
	ErrorRate float64
	ErrorHigh bool
	P50       float64
	P95       float64
	P99       float64
	Classes   []statusClassView
	Codes     []statusCodeView
}

// errorClasses is the set of status classes that count toward the headline error
// rate, matching the CONTEXT Error definition.
var errorClasses = map[string]bool{
	"4xx":       true,
	"5xx":       true,
	"no_status": true,
}

// toDetailView maps the storage EndpointDetail into the rendered headline view,
// marking which classes are errors and sorting the exact codes.
func toDetailView(d storage.EndpointDetail) detailView {
	v := detailView{
		Count:     d.Count,
		ErrorRate: d.ErrorRate,
		ErrorHigh: d.ErrorRate >= errorRateWarn,
		P50:       d.P50,
		P95:       d.P95,
		P99:       d.P99,
	}
	for _, c := range d.StatusClasses {
		v.Classes = append(v.Classes, statusClassView{
			Class:   c.Class,
			Count:   c.Count,
			IsError: errorClasses[c.Class],
		})
	}
	for code, count := range d.StatusCodes {
		v.Codes = append(v.Codes, statusCodeView{Code: code, Count: count})
	}
	sort.Slice(v.Codes, func(i, j int) bool { return v.Codes[i].Code < v.Codes[j].Code })
	return v
}

// keyRow is one rendered row of the Setup keys table: the label, a masked
// last-4 fragment, the issue date, and whether the key is revoked (revoked keys
// drop the revoke action).
type keyRow struct {
	ID      string
	Label   string
	Masked  string // "····<last4>", the only fragment of the secret we can show
	Created string // date only; the list is a ledger, not a timeline
	Revoked bool
}

// toKeyRows maps the control-plane key infos into display rows, masking the
// last-4 and formatting the issue date. Order is preserved (newest first).
func toKeyRows(infos []KeyInfo) []keyRow {
	out := make([]keyRow, 0, len(infos))
	for _, k := range infos {
		out = append(out, keyRow{
			ID:      k.ID,
			Label:   k.Label,
			Masked:  "····" + k.Last4,
			Created: k.CreatedAt.Format("2006-01-02"),
			Revoked: k.RevokedAt != nil,
		})
	}
	return out
}

// debugContext is the shareable triage block on the endpoint-detail page: the
// exact coordinates of what is being looked at (service, method, route), the
// precise window bounds, the dominant error class, and the tail latencies.
// Summary is the one-line copyable form; the page URL is the shareable link.
type debugContext struct {
	Service     string
	Method      string
	Route       string
	From        string // exact window start (UTC)
	To          string // exact window end (UTC)
	DominantErr string // highest-count error class, or "none"
	P95         string
	P99         string
	Summary     string // one-line, copy-to-clipboard
}

// debugTimeLayout formats the window bounds to second precision in UTC so a
// pasted debug context is unambiguous across timezones.
const debugTimeLayout = "2006-01-02 15:04:05 UTC"

// buildDebugContext assembles the detail-page debug block from the request
// coordinates, the exact window bounds, and the detail view.
func buildDebugContext(service, method, route string, from, to time.Time, dv detailView) debugContext {
	fromS := from.UTC().Format(debugTimeLayout)
	toS := to.UTC().Format(debugTimeLayout)
	dom := dominantErrorClass(dv.Classes)
	p95, p99 := fmtMsFull(dv.P95), fmtMsFull(dv.P99)
	return debugContext{
		Service:     service,
		Method:      method,
		Route:       route,
		From:        fromS,
		To:          toS,
		DominantErr: dom,
		P95:         p95,
		P99:         p99,
		Summary: fmt.Sprintf("%s · %s %s · %s → %s · dominant error %s · p95 %s · p99 %s",
			service, method, route, fromS, toS, dom, p95, p99),
	}
}

// dominantErrorClass returns the highest-count error class (4xx/5xx/no_status),
// or "none" when the endpoint has no errors in the window.
func dominantErrorClass(classes []statusClassView) string {
	best := ""
	var bestCount uint64
	for _, c := range classes {
		if c.IsError && c.Count > bestCount {
			best, bestCount = c.Class, c.Count
		}
	}
	if best == "" {
		return "none"
	}
	return best
}

// onboardingStep is one of the 4 CONTEXT onboarding steps with its done state
// and a short human label.
type onboardingStep struct {
	Label string
	Done  bool
}

// onboardingData drives the onboarding panel template: the 4 steps, the list of
// connected sources (if any), and the frozen-cardinality warning.
type onboardingData struct {
	Steps     []onboardingStep
	Connected []ServiceOnboarding
	Frozen    bool
}

// buildOnboarding derives the live 4-step state (CONTEXT Handshake) from the
// handshake list and the frozen flag:
//
//	step 1 key valid          — always true here (this page is only reachable
//	                            after the tenant resolved, i.e. a valid key);
//	step 2 service connected  — at least one handshake row exists;
//	step 3 waiting for Summary — a service connected but no summary yet (this
//	                            panel is only shown when the tenant has NO data,
//	                            so once connected we are, by definition, waiting);
//	step 4 first data received — false here (renderOnboarding is only reached
//	                            when HasAnySummary is false).
//
// It never invents data: clock-skew drops and bad-key rejections are surfaced
// only where a real per-tenant signal exists; there is no per-tenant skew
// counter yet, so that line is omitted rather than faked (Part-2 follow-up).
func buildOnboarding(connected []ServiceOnboarding, frozen bool) onboardingData {
	serviceConnected := len(connected) > 0
	return onboardingData{
		Steps: []onboardingStep{
			{Label: "Ingest key valid", Done: true},
			{Label: "Service connected", Done: serviceConnected},
			{Label: "Waiting for first Summary", Done: serviceConnected},
			{Label: "First data received", Done: false},
		},
		Connected: connected,
		Frozen:    frozen,
	}
}
