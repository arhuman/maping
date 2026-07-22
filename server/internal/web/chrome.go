package web

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// This file holds the dashboard chrome that the dark design needs: the Shell
// (sidebar + top-bar state), the window switcher, the sidebar nav, and the
// KPI/stat/status-bar builders. Everything here is I/O-free, so it is
// unit-testable without HTTP or ClickHouse. The pure value formatters and
// colour-class helpers live alongside in chrome_format.go.

// ---------------------------------------------------------------- shell types

// Shell is the chrome shared by every page: the sidebar identity + nav and the
// top bar (breadcrumbs, title, window switcher). Each page's view data embeds a
// Shell so the shared sidebar/topbar sub-templates render from one place.
type Shell struct {
	Org, User, Role string
	// AccountHref, when non-empty, makes the sidebar user-identity block a link to
	// the composing build's account page. Empty leaves it a display-only element.
	AccountHref  string
	Nav          []navItem
	Crumbs       []crumb
	PageTitle    string
	ShowControls bool
	Windows      []windowOption
	WindowKey    string
	FlushLabel   string
	// KeyMask is the masked last-4 ("····<last4>") of the tenant's newest active
	// ingest key, shown in the sidebar. Empty when there is no control plane or no
	// active key, so the sidebar shows a muted "no active key" instead of a fake.
	KeyMask string
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
// query params (e.g. ?sort=) so switching the window never drops the sort. A
// preset switch drops any custom detail range (?from=&to=): picking 5m/1h/24h is
// the reset back to a preset lookback.
func buildWindows(r *http.Request, active string) []windowOption {
	out := make([]windowOption, 0, len(windowKeys))
	for _, k := range windowKeys {
		q := r.URL.Query()
		q.Set("win", k)
		q.Del("from")
		q.Del("to")
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

// withWin appends the active window to an href so the selected lookback survives
// navigation (nav, breadcrumbs, drill-downs). winKey is always an allowlisted key
// (normalizeWindow guarantees it), so no escaping is needed; the separator adapts
// to whether the href already carries a query (e.g. a drill's ?sort=). An empty
// winKey (no shell window) leaves the href untouched.
func withWin(href, winKey string) string {
	if winKey == "" {
		return href
	}
	sep := "?"
	if strings.Contains(href, "?") {
		sep = "&"
	}
	return href + sep + "win=" + winKey
}

// buildNav builds the sidebar nav with the active item highlighted. active is
// one of "overview", "performance", "setup"; the endpoint/detail levels pass
// "overview" so Services stays lit while drilling down. Setup owns both keys
// management and the handshake stepper, so there is no separate "Ingest keys"
// item. winKey is threaded onto every href so switching pages keeps the lookback.
func buildNav(active, winKey string) []navItem {
	items := []navItem{
		{Label: "Services", Icon: "▦", Href: withWin("/", winKey), Badge: ""},
		{Label: "Performance", Icon: "◈", Href: withWin("/performance", winKey), Badge: ""},
		{Label: "Setup", Icon: "✦", Href: withWin("/setup", winKey), Badge: ""},
		// Documentation opens /doc, which renders inside this dashboard chrome for a
		// signed-in user (the docs handler routes authenticated requests through
		// RenderDocPage, activeNav "docs") and in its own standalone shell otherwise.
		// The window param is not threaded onto it: doc pages have no lookback.
		{Label: "Documentation", Icon: "❖", Href: "/doc", Badge: ""},
	}
	navKey := map[string]string{
		"Services": "overview", "Performance": "performance", "Setup": "setup",
		"Documentation": "docs",
	}
	for i := range items {
		items[i].Active = navKey[items[i].Label] == active
	}
	return items
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
		{Label: "SPREAD", Value: fmtSpread(d.P50, d.P95), ColorClass: spreadClass(d.P50, d.P95)},
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
