package web

import (
	"html/template"
	"strconv"
	"strings"
)

// fmtFloat formats f with prec decimal places, trimming to a plain decimal
// (no scientific notation) so table cells read cleanly.
func fmtFloat(f float64, prec int) string {
	return strconv.FormatFloat(f, 'f', prec, 64)
}

// initials returns the up-to-two-letter uppercase avatar initials for a name.
func initials(name string) string {
	fields := strings.Fields(name)
	switch len(fields) {
	case 0:
		return "?"
	case 1:
		r := []rune(fields[0])
		if len(r) == 1 {
			return strings.ToUpper(string(r))
		}
		return strings.ToUpper(string(r[:2]))
	default:
		return strings.ToUpper(string([]rune(fields[0])[:1]) + string([]rune(fields[len(fields)-1])[:1]))
	}
}

// ---------------------------------------------------------------- view data

// overviewData feeds the service-overview page (level 1): the KPI strip and the
// services table, plus the frozen-cardinality banner.
type overviewData struct {
	Shell       Shell
	Frozen      bool
	KPIs        []kpi
	Services    []serviceRow
	WindowLabel string
}

// endpointsData feeds the endpoint-table page (level 2).
type endpointsData struct {
	Shell     Shell
	Service   string
	Sort      string
	Frozen    bool
	KPIs      []kpi
	Endpoints []endpointRow
}

// detailData feeds the endpoint-detail page (level 3): the RED stat cards, the
// server-rendered time-series and histogram SVGs, and the status breakdown.
type detailData struct {
	Shell      Shell
	Service    string
	Method     string
	Route      string
	Detail     detailView
	Stats      []kpi
	StatusBars []statusBar
	Debug      debugContext
	TSChart    template.HTML
	HistChart  template.HTML
}

// onboardingPage feeds the onboarding page: the live handshake stepper, the
// connected sources, and the frozen warning.
type onboardingPage struct {
	Shell     Shell
	Steps     []onbStepView
	Connected []ServiceOnboarding
	Frozen    bool
	Refresh   bool // emit the 3s handshake meta-refresh (onboarding incomplete)
}

// performancePage feeds the (static) performance/architecture page.
type performancePage struct {
	Shell Shell
}

// setupPage feeds the always-reachable Setup page: the live handshake stepper
// (reused onboarding views) and, when a control plane is present, the self-serve
// ingest-keys panel. NewToken carries the reveal-once plaintext right after a
// create; CSRFToken signs the create/revoke forms.
type setupPage struct {
	Shell     Shell
	Steps     []onbStepView
	Connected []ServiceOnboarding
	Frozen    bool
	ShowKeys  bool // false when no control plane: the keys panel is hidden.
	Keys      []keyRow
	NewToken  string // non-empty only on the create POST response (reveal once).
	CSRFToken string
	Refresh   bool // emit the 3s handshake meta-refresh (onboarding incomplete)
}
