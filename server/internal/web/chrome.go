package web

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// This file holds the dashboard chrome and the pure presentation formatters that
// the dark design needs: the Shell (sidebar + top-bar state), the KPI/stat/
// status-bar builders, the window switcher, and the value/colour-class helpers.
// Everything here is I/O-free, so it is unit-testable without HTTP or ClickHouse,
// and colours are emitted as CSS class names (never dynamic inline colours) so
// html/template never has to filter a var() out of a style attribute.

// ---------------------------------------------------------------- shell types

// Shell is the chrome shared by every page: the sidebar identity + nav and the
// top bar (breadcrumbs, title, window switcher). Each page's view data embeds a
// Shell so the shared sidebar/topbar sub-templates render from one place.
type Shell struct {
	Org, User, Role string
	Nav             []navItem
	Crumbs          []crumb
	PageTitle       string
	ShowControls    bool
	Windows         []windowOption
	WindowKey       string
	FlushLabel      string
	// Live drives the casual-check auto-refresh: when true the page emits a
	// meta-refresh and the top-bar indicator reads "live". LiveHref is the toggle
	// link (set only on pages that support live refresh — the overview); an empty
	// LiveHref renders the static, non-clickable indicator.
	Live     bool
	LiveHref string
}

// navItem is one sidebar entry with its active state and optional badge.
type navItem struct {
	Label, Icon, Href, Badge string
	Active                   bool
}

// crumb is one breadcrumb segment; a non-empty Href renders it as a link.
type crumb struct {
	Label string
	Href  string
}

// windowOption is one lookback choice in the top-bar switcher.
type windowOption struct {
	Key, Href string
	Active    bool
}

// kpi is one metric card (overview KPI strip, endpoint svc-KPIs, detail stats).
type kpi struct {
	Label, Value, Unit, Sub, ColorClass string
}

// statusBar is one class row in the endpoint-detail breakdown (2xx…timeout).
type statusBar struct {
	Label                string
	Count                uint64
	Pct                  string
	LabelClass, BarClass string
}

// onbStepView is one rendered onboarding step: its glyph, dot state, and copy.
type onbStepView struct {
	Icon, DotClass, LabelClass, Label, Sub string
	Connector                              bool
}

// ------------------------------------------------------------------- windows

// windowKeys is the allowlisted lookback set; an unknown ?win= falls back to 1h.
var windowKeys = []string{"5m", "1h", "24h"}

var windowDur = map[string]time.Duration{
	"5m":  5 * time.Minute,
	"1h":  time.Hour,
	"24h": 24 * time.Hour,
}

var windowText = map[string]string{
	"5m":  "5 min",
	"1h":  "1 hour",
	"24h": "24 hours",
}

// normalizeWindow maps a raw ?win= param to an allowlisted key, defaulting to
// 1h. This is the guard: only these three keys ever drive the lookback.
func normalizeWindow(raw string) string {
	if _, ok := windowDur[raw]; ok {
		return raw
	}
	return "1h"
}

// windowRange returns the [from, to) lookback ending now for a duration.
func windowRange(d time.Duration) (from, to time.Time) {
	to = time.Now().UTC()
	from = to.Add(-d)
	return from, to
}

// buildWindows builds the switcher options, preserving the request's other
// query params (e.g. ?sort=) so switching the window never drops the sort.
func buildWindows(r *http.Request, active string) []windowOption {
	out := make([]windowOption, 0, len(windowKeys))
	for _, k := range windowKeys {
		q := r.URL.Query()
		q.Set("win", k)
		out = append(out, windowOption{Key: k, Href: r.URL.Path + "?" + q.Encode(), Active: k == active})
	}
	return out
}

// isLive reports whether the casual-check auto-refresh is opted in via ?live=1.
func isLive(r *http.Request) bool { return r.URL.Query().Get("live") == "1" }

// liveToggleHref builds the link that flips the ?live opt-in, preserving the
// other query params (e.g. ?win=) so toggling live never drops the window.
func liveToggleHref(r *http.Request, on bool) string {
	q := r.URL.Query()
	if on {
		q.Del("live")
	} else {
		q.Set("live", "1")
	}
	if enc := q.Encode(); enc != "" {
		return r.URL.Path + "?" + enc
	}
	return r.URL.Path
}

// -------------------------------------------------------------------- navbar

// buildNav builds the sidebar nav with the active item highlighted. active is
// one of "overview", "performance", "setup"; the endpoint/detail levels pass
// "overview" so Services stays lit while drilling down. Setup owns both keys
// management and the handshake stepper, so there is no separate "Ingest keys"
// item.
func buildNav(active string) []navItem {
	items := []navItem{
		{Label: "Services", Icon: "▦", Href: "/", Badge: ""},
		{Label: "Performance", Icon: "◈", Href: "/performance", Badge: ""},
		{Label: "Setup", Icon: "✦", Href: "/setup", Badge: ""},
	}
	navKey := map[string]string{
		"Services": "overview", "Performance": "performance", "Setup": "setup",
	}
	for i := range items {
		items[i].Active = navKey[items[i].Label] == active
	}
	return items
}

// ---------------------------------------------------------------- formatters

// fmtRate renders a per-second rate: 2103 -> "2.1k", 903 -> "903".
func fmtRate(r float64) string {
	if r >= 1000 {
		return strings.TrimSuffix(strconv.FormatFloat(r/1000, 'f', 1, 64), ".0") + "k"
	}
	return strconv.FormatFloat(r, 'f', 0, 64)
}

// fmtCount renders a request total: 4.2M / 12.0k / 830.
func fmtCount(c uint64) string {
	f := float64(c)
	switch {
	case f >= 1e6:
		return strconv.FormatFloat(f/1e6, 'f', 1, 64) + "M"
	case f >= 1e3:
		return strconv.FormatFloat(f/1e3, 'f', 1, 64) + "k"
	default:
		return strconv.FormatUint(c, 10)
	}
}

// fmtPctD renders a [0,1] fraction as a percentage with 1 decimal at/above 10%
// and 2 below, matching the design (0.021 -> "2.10%", 0.163 -> "16.3%").
func fmtPctD(f float64) string {
	dec := 2
	if f >= 0.1 {
		dec = 1
	}
	return strconv.FormatFloat(f*100, 'f', dec, 64) + "%"
}

// fmtMsVal / fmtMsUnit split a seconds value into the design's number + unit:
// >=1s -> "1.18" / "s"; <10ms -> "2.0" / "ms"; else "88" / "ms".
func fmtMsVal(sec float64) string {
	ms := sec * 1000
	switch {
	case ms >= 1000:
		return strconv.FormatFloat(ms/1000, 'f', 2, 64)
	case ms < 10:
		return strconv.FormatFloat(ms, 'f', 1, 64)
	default:
		return strconv.FormatFloat(ms, 'f', 0, 64)
	}
}

func fmtMsUnit(sec float64) string {
	if sec*1000 >= 1000 {
		return "s"
	}
	return "ms"
}

// fmtMsFull is the combined "value unit" form for table cells ("88 ms").
func fmtMsFull(sec float64) string { return fmtMsVal(sec) + " " + fmtMsUnit(sec) }

// ------------------------------------------------------------- colour classes

// errClass colours an error-rate value: >=5% error, >=2% warn, else muted.
func errClass(f float64) string {
	switch {
	case f >= 0.05:
		return "c-err"
	case f >= 0.02:
		return "c-warn"
	default:
		return "c-txt2"
	}
}

// p99Class flags a slow p99 (>=600ms) so the cell reads warn.
func p99Class(sec float64) string {
	if sec >= 0.6 {
		return "c-warn"
	}
	return "c-txt2"
}

// healthClass picks the service health dot from its error rate.
func healthClass(f float64) string {
	switch {
	case f >= 0.05:
		return "dot-err"
	case f >= 0.02:
		return "dot-warn"
	default:
		return "dot-ok"
	}
}

// methodClass maps an HTTP method to its chip colour class.
func methodClass(m string) string {
	switch m {
	case "GET":
		return "m-get"
	case "POST":
		return "m-post"
	case "DELETE":
		return "m-delete"
	case "PUT":
		return "m-put"
	case "PATCH":
		return "m-patch"
	default:
		return "m-other"
	}
}

// codeClass colours an exact status code by its class.
func codeClass(code uint32) string {
	switch {
	case code >= 200 && code < 300:
		return "c-ok"
	case code < 400:
		return "c-blue"
	case code < 500:
		return "c-warn"
	default:
		return "c-err"
	}
}

// classBarClass colours a status-breakdown bar fill by class.
func classBarClass(class string) string {
	switch class {
	case "2xx":
		return "bar-ok"
	case "3xx":
		return "bar-blue"
	case "4xx":
		return "bar-warn"
	case "5xx", "no_status":
		return "bar-err"
	default:
		return "bar-txt3"
	}
}

// ------------------------------------------------------------- kpi/stat build

// overviewKPIs derives the service-overview KPI strip from the rendered service
// rows: total traffic, request count over the window, traffic-weighted error
// rate, and worst p99. All real — no invented period-over-period deltas.
func overviewKPIs(rows []serviceRow, winLabel string) []kpi {
	var totalRate, errNum, worstP99 float64
	var totalCount uint64
	for _, s := range rows {
		totalRate += s.RatePerSec
		totalCount += s.Count
		errNum += s.ErrorRate * s.RatePerSec
		if s.P99 > worstP99 {
			worstP99 = s.P99
		}
	}
	wErr := 0.0
	if totalRate > 0 {
		wErr = errNum / totalRate
	}
	return []kpi{
		{Label: "TOTAL TRAFFIC", Value: fmtRate(totalRate), Unit: "req/s", ColorClass: "c-txt", Sub: "across " + strconv.Itoa(len(rows)) + " services"},
		{Label: "REQUESTS (" + winLabel + ")", Value: fmtCount(totalCount), ColorClass: "c-txt"},
		{Label: "ERROR RATE", Value: fmtPctD(wErr), ColorClass: errClass(wErr)},
		{Label: "WORST p99", Value: fmtMsVal(worstP99), Unit: fmtMsUnit(worstP99), ColorClass: p99Class(worstP99)},
	}
}

// endpointKPIs derives the endpoint-table svc-KPIs (count, traffic, error rate)
// from the rendered endpoint rows for the service.
func endpointKPIs(rows []endpointRow) []kpi {
	var totalRate, errNum float64
	for _, e := range rows {
		totalRate += e.RatePerSec
		errNum += e.ErrorRate * e.RatePerSec
	}
	wErr := 0.0
	if totalRate > 0 {
		wErr = errNum / totalRate
	}
	return []kpi{
		{Label: "ENDPOINTS", Value: strconv.Itoa(len(rows)), ColorClass: "c-txt"},
		{Label: "TRAFFIC", Value: fmtRate(totalRate), Unit: "req/s", ColorClass: "c-txt"},
		{Label: "ERROR RATE", Value: fmtPctD(wErr), ColorClass: errClass(wErr)},
	}
}

// detailStats builds the endpoint-detail headline RED cards from the detail view
// and the window length (rate = count / window-seconds).
func detailStats(d detailView, winSeconds float64) []kpi {
	rate := 0.0
	if winSeconds > 0 {
		rate = float64(d.Count) / winSeconds
	}
	return []kpi{
		{Label: "RATE", Value: fmtRate(rate), Unit: "req/s", ColorClass: "c-txt"},
		{Label: "ERROR RATE", Value: fmtPctD(d.ErrorRate), ColorClass: errClass(d.ErrorRate)},
		{Label: "p50", Value: fmtMsVal(d.P50), Unit: fmtMsUnit(d.P50), ColorClass: "c-txt"},
		{Label: "p95", Value: fmtMsVal(d.P95), Unit: fmtMsUnit(d.P95), ColorClass: "c-txt"},
		{Label: "p99", Value: fmtMsVal(d.P99), Unit: fmtMsUnit(d.P99), ColorClass: p99Class(d.P99)},
	}
}

// statusBarsFor builds the class-breakdown bars (width = class count / total).
func statusBarsFor(d detailView) []statusBar {
	out := make([]statusBar, 0, len(d.Classes))
	for _, c := range d.Classes {
		pct := 0.0
		if d.Count > 0 {
			pct = float64(c.Count) / float64(d.Count) * 100
		}
		labelClass := "c-txt2"
		if c.IsError {
			labelClass = "c-txt"
		}
		out = append(out, statusBar{
			Label:      c.Class,
			Count:      c.Count,
			Pct:        strconv.FormatFloat(pct, 'f', 1, 64) + "%",
			LabelClass: labelClass,
			BarClass:   classBarClass(c.Class),
		})
	}
	return out
}

// onboardingStepViews renders the handshake stepper from the live Done flags:
// every done step is a ✓, the first not-done step is the in-progress ◐, and the
// rest are numbered. Subs are static per step (the design copy).
func onboardingStepViews(steps []onboardingStep) []onbStepView {
	subs := []string{
		"ingest key resolved",
		"handshake received",
		"flush window ~10s (accelerated on cold start)",
		"RED metrics live",
	}
	current := -1
	for i, s := range steps {
		if !s.Done {
			current = i
			break
		}
	}
	out := make([]onbStepView, 0, len(steps))
	for i, s := range steps {
		sub := ""
		if i < len(subs) {
			sub = subs[i]
		}
		v := onbStepView{Label: s.Label, Sub: sub, Connector: i < len(steps)-1}
		switch {
		case s.Done:
			v.Icon, v.DotClass, v.LabelClass = "✓", "dot-done", "c-txt"
		case i == current:
			v.Icon, v.DotClass, v.LabelClass = "◐", "dot-current", "c-txt"
		default:
			v.Icon, v.DotClass, v.LabelClass = strconv.Itoa(i+1), "dot-todo", "c-txt3"
		}
		out = append(out, v)
	}
	return out
}
