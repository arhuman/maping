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
// server-rendered time-series and histogram SVGs, the status breakdown, the
// instance-outlier panel (per-replica RED, to spot a bad replica), the exemplars
// panel (real captured requests, to pivot from a spike to a trace), and the
// success-vs-error latency split (p50/p95/p99 per status class).
type detailData struct {
	Shell           Shell
	Service         string
	Method          string
	Route           string
	Detail          detailView
	Verdict         verdictView
	Stats           []kpi
	StatusBars      []statusBar
	Debug           debugContext
	Range           detailRange
	TSChart         template.HTML
	HistChart       template.HTML
	Instances       []instanceStatRow
	Versions        []versionRow
	Exemplars       []exemplarRow
	ClassLatency    []classLatencyRow
	ErrorClasses    []errorClassRow
	NoStatusReasons []noStatusReasonRow
	Downstream      downstreamView
	Resources       []resourceRow
}

// handshakeView feeds the live handshake stepper partial ("handshake-stepper"),
// shared by the get-started page, the Setup page, and the /setup/handshake polling
// fragment. Complete is true once the first Summary is ingested (HasAnySummary),
// which is the signal that stops the client polling.
type handshakeView struct {
	Steps     []onbStepView
	Connected []ServiceOnboarding
	Frozen    bool
	Complete  bool
}

// onboardingPage feeds the onboarding page: the live handshake stepper, the
// connected sources, and the frozen warning.
type onboardingPage struct {
	Shell     Shell
	Handshake handshakeView
	Frozen    bool
	Refresh   bool   // gate the <noscript> meta-refresh fallback (onboarding incomplete)
	Framework string // which wire-up snippet the selector card shows checked (?fw=, default "gin")
}

// performancePage feeds the performance/architecture page. The volume figures
// (Requests…Ratio) are derived from the tenant's real stored summaries over the
// last 24h; HasData is false when there is nothing to show yet. The rollup-tier
// retention and the ingestion-path diagram remain static architecture facts.
type performancePage struct {
	Shell Shell

	HasData     bool
	Requests    string // represented requests, compact (e.g. "1.2M")
	Summaries   string // shipped summary rows, compact
	Compression string // requests per summary, e.g. "4.4k×"
	IngestRate  string // window-average represented requests/s, e.g. "182k/s"
	QueryMs     string // measured latency of this page's own aggregate query

	// WindowLabel is the prose lookback ("24 hours"/"1 hour"/"5 min") and
	// WindowShort its compact chip form ("24H"/"1H"/"5M"), so the volume figures
	// name the window the operator selected rather than a fixed "24h".
	WindowLabel string
	WindowShort string

	RawDisk       string // projected raw-event-pipeline size, e.g. "47.2 GB"
	SummaryDisk   string // measured/estimated summaries size, e.g. "0.61 GB"
	SummaryBarPct string // CSS width of the summaries bar vs the raw bar
	Ratio         string // disk reduction factor, e.g. "77×" (or "—")
	RawEventBytes int    // documented per-event assumption, shown in the footnote

	// Tiers is the rollup-tier retention ladder (static architecture facts derived
	// from the real migration TTLs), always populated regardless of HasData.
	Tiers []rollupTier
}

// setupPage feeds the always-reachable Setup page: the live handshake stepper
// (reused onboarding views) and, when a control plane is present, the self-serve
// ingest-keys panel. NewToken carries the reveal-once plaintext right after a
// create; CSRFToken signs the create/revoke forms.
type setupPage struct {
	Shell     Shell
	Handshake handshakeView
	Frozen    bool
	ShowKeys  bool // false when no control plane: the keys panel is hidden.
	Keys      []keyRow
	NewToken  string // non-empty only on the create POST response (reveal once).
	CSRFToken string
	Refresh   bool // gate the <noscript> meta-refresh fallback (onboarding incomplete)

	// Team panel (members + invites). ShowTeam is false when no control plane.
	ShowTeam  bool
	IsAdmin   bool // gates the invite/remove forms in the template.
	SeatUsed  int
	SeatLimit int
	Members   []memberRow
	Invites   []inviteRow
	InviteURL string // reveal-once accept link right after creating an invite.
	TeamError string // e.g. "seat limit reached" after a failed invite.
}

// memberRow is a rendered org member in the team panel.
type memberRow struct {
	ID      string
	Email   string
	Role    string
	IsOwner bool
	Created string // "2006-01-02"
}

// inviteRow is a rendered pending invite in the team panel.
type inviteRow struct {
	ID      string
	Email   string
	Role    string
	Expires string // "2006-01-02"
}
