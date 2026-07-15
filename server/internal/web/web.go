// Package web serves the mAPI-ng dashboard: the fixed, non-configurable, auto-
// generated 3-level RED view (CONTEXT Dashboard) plus the live 4-step onboarding
// panel driven by the handshake. It is server-rendered HTML (html/template
// autoescaping) with no client-side JavaScript framework: a strict script-src 'self'
// CSP (ADR-0008) rules out CDN scripts, so the time-series and latency histogram charts
// are inline server-rendered SVG (see chart.go). The only script served is a self-hosted
// assets/copy.js. The /api/series and /api/histogram JSON endpoints remain in the code
// but are not consumed by the current UI.
//
// The three levels are:
//
//  1. GET /                      service overview (rate/error%/p50/p95/p99 per service)
//  2. GET /services/{service}    endpoint table, server-side sortable via ?sort=
//  3. GET /services/{service}/endpoint?method=&route=   endpoint detail + histogram
//
// The active tenant is resolved PER REQUEST via an injected func, never
// hardcoded: for Part 1 main supplies a constant dev-tenant func, and Part 2
// (auth) will swap in the authenticated org — this is the seam that keeps auth
// out of Part 1. The onboarding source and cardinality-frozen check are also
// injected and nil-safe, so the dashboard still renders when there is no control
// plane (dev-without-Postgres).
package web

import (
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"time"

	"github.com/arhuman/maping/server/internal/storage"
	"github.com/arhuman/maping/server/internal/tenant"
)

// Handler serves the 3-level dashboard, the onboarding panel, and the JSON data
// endpoints. Every dependency beyond the querier is injected and nil-safe.
type Handler struct {
	q          Querier
	tenant     TenantResolver
	onboarding OnboardingSource // may be nil (no control plane).
	frozen     FrozenFunc       // may be nil (no guardrail signal).
	keys       KeyAdmin         // may be nil (no control plane): hides the keys panel.
	members    MemberAdmin      // may be nil (no control plane): hides the team panel.
	roleOf     RoleResolver     // may be nil: per-request role for admin-gating team actions.
	csrf       *csrf            // nil when keys/members are nil; guards the Setup POSTs.
	org        string           // sidebar identity chrome (display only).
	user       string
	role       string
	log        *slog.Logger
	tpl        *template.Template
}

// Config bundles the Handler dependencies so NewHandler's signature stays
// readable as the injected surface grows (tenant, onboarding, frozen).
type Config struct {
	Querier    Querier
	Tenant     TenantResolver
	Onboarding OnboardingSource
	Frozen     FrozenFunc
	// KeyAdmin drives the self-serve Setup keys panel. Nil (dev/no-control-plane)
	// hides the panel and 404s the key POSTs.
	KeyAdmin KeyAdmin
	// MemberAdmin drives the self-serve Setup team panel (members + invites). Nil
	// (dev/no-control-plane) hides the panel and 404s its POSTs.
	MemberAdmin MemberAdmin
	// Role resolves the caller's role per request, so the team panel can admin-gate
	// its create/revoke/remove actions. Nil is treated as "not an admin".
	Role RoleResolver
	// CSRFKey signs the Setup form CSRF tokens (HMAC). Required (>= 1 byte) when
	// KeyAdmin or MemberAdmin is set; ignored otherwise. main passes the session key.
	CSRFKey []byte
	Logger  *slog.Logger
	// Sidebar identity chrome (display only). Empty values fall back to
	// sensible defaults so the dashboard renders without a control plane.
	OrgName  string
	UserName string
	UserRole string
}

// NewHandler builds the dashboard Handler. Querier and Tenant are required;
// Onboarding and Frozen are optional (nil-safe) so the dashboard renders without
// a control plane or guardrail signal.
func NewHandler(cfg Config) (*Handler, error) {
	if cfg.Querier == nil {
		return nil, fmt.Errorf("web.NewHandler: nil Querier")
	}
	if cfg.Tenant == nil {
		return nil, fmt.Errorf("web.NewHandler: nil Tenant resolver")
	}
	if (cfg.KeyAdmin != nil || cfg.MemberAdmin != nil) && len(cfg.CSRFKey) == 0 {
		return nil, fmt.Errorf("web.NewHandler: KeyAdmin/MemberAdmin set without a CSRFKey")
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	tpl, err := parseTemplates()
	if err != nil {
		return nil, fmt.Errorf("web.NewHandler: parse templates: %w", err)
	}
	return &Handler{
		q:          cfg.Querier,
		tenant:     cfg.Tenant,
		onboarding: cfg.Onboarding,
		frozen:     cfg.Frozen,
		keys:       cfg.KeyAdmin,
		members:    cfg.MemberAdmin,
		roleOf:     cfg.Role,
		csrf:       newCSRF(cfg.CSRFKey),
		org:        orDefault(cfg.OrgName, "mAPI-ng"),
		user:       orDefault(cfg.UserName, "dev"),
		role:       orDefault(cfg.UserRole, "admin"),
		log:        log,
		tpl:        tpl,
	}, nil
}

// orDefault returns v when non-empty, else def.
func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// Register mounts the dashboard routes on mux. /dashboard aliases / so both
// URLs resolve to the overview.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /", h.serveOverview)
	mux.HandleFunc("GET /dashboard", h.serveOverview)
	mux.HandleFunc("GET /performance", h.servePerformance)
	mux.HandleFunc("GET /setup", h.serveSetup)
	mux.HandleFunc("POST /setup/keys", h.serveCreateKey)
	mux.HandleFunc("POST /setup/keys/{id}/revoke", h.serveRevokeKey)
	mux.HandleFunc("POST /setup/invites", h.serveCreateInvite)
	mux.HandleFunc("POST /setup/invites/{id}/revoke", h.serveRevokeInvite)
	mux.HandleFunc("POST /setup/members/{id}/remove", h.serveRemoveMember)
	mux.HandleFunc("GET /services/{service}", h.serveEndpoints)
	mux.HandleFunc("GET /services/{service}/endpoint", h.serveEndpointDetail)

	mux.HandleFunc("GET /api/series", h.serveSeries)
	mux.HandleFunc("GET /api/histogram", h.serveHistogram)
	mux.HandleFunc("GET /api/instances", h.serveInstances)

	mux.HandleFunc("GET /assets/copy.js", h.serveCopyJS)
}

// buildShell assembles the per-request chrome (sidebar identity + nav, top-bar
// breadcrumbs/title/window switcher) shared by every page.
func (h *Handler) buildShell(r *http.Request, activeNav string, crumbs []crumb, title string, showControls bool, winKey string) Shell {
	return Shell{
		Org:          h.org,
		User:         h.user,
		Role:         h.role,
		Nav:          buildNav(activeNav, winKey),
		Crumbs:       crumbs,
		PageTitle:    title,
		ShowControls: showControls,
		Windows:      buildWindows(r, winKey),
		WindowKey:    winKey,
		FlushLabel:   "flush 10s",
		KeyMask:      h.activeKeyMask(r),
	}
}

// activeKeyMask returns the masked last-4 ("····<last4>") of the tenant's newest
// active ingest key for the sidebar, or "" when there is no control plane, the
// tenant does not resolve, the lookup fails, or no active key exists. It never
// 401s or errors — the sidebar is chrome, so any miss degrades to an empty mask
// (a muted "no active key") rather than blocking the page. ListKeys returns keys
// newest-first, so the first non-revoked entry is the current one.
func (h *Handler) activeKeyMask(r *http.Request) string {
	if h.keys == nil {
		return ""
	}
	tid, ok := h.tenant(r)
	if !ok {
		return ""
	}
	infos, err := h.keys.ListKeys(r.Context(), tid.String())
	if err != nil {
		h.log.Error("web: sidebar key lookup", slog.Any("err", err))
		return ""
	}
	for _, k := range infos {
		if k.RevokedAt == nil {
			return "····" + k.Last4
		}
	}
	return ""
}

// servePerformance renders the performance/architecture page. The volume and
// compression figures are computed from the tenant's real stored summaries over
// the selected window (the shared top-bar switcher, shown here too), so the page
// honours the same lookback as the rest of the dashboard; a query failure logs
// and renders the waiting-for-data state rather than 500-ing. The rollup-tier
// retention and ingestion-path diagram are static architecture facts.
func (h *Handler) servePerformance(w http.ResponseWriter, r *http.Request) {
	tid, ok := h.resolveTenant(w, r)
	if !ok {
		return
	}
	winKey := normalizeWindow(r.URL.Query().Get("win"))
	dur := windowDur[winKey]
	shell := h.buildShell(r, "performance", []crumb{{Label: "performance"}}, "Performance", true, winKey)

	from, to := windowRange(dur)
	start := time.Now()
	stat, err := h.q.Tenant(tid).PerformanceStats(r.Context(), from, to)
	queryDur := time.Since(start)
	if err != nil {
		h.log.Error("web: performance stats", slog.Any("err", err))
		stat = storage.PerformanceStat{}
	}

	h.render(w, "performance", buildPerformance(shell, stat, dur, queryDur, winKey))
}

// resolveTenant resolves the active tenant or writes a 401. It centralizes the
// auth seam so Part 2 only changes the injected resolver, not every handler.
func (h *Handler) resolveTenant(w http.ResponseWriter, r *http.Request) (tenant.ID, bool) {
	tid, ok := h.tenant(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return tenant.ID{}, false
	}
	return tid, true
}

// serveOverview renders level 1: the service table, or — when the tenant has no
// summary data yet — the onboarding panel. The "/" route also matches unknown
// paths under ServeMux's pattern matching, so a not-found path outside the known
// set returns 404 rather than the overview.
func (h *Handler) serveOverview(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/dashboard" {
		http.NotFound(w, r)
		return
	}
	tid, ok := h.resolveTenant(w, r)
	if !ok {
		return
	}
	winKey := normalizeWindow(r.URL.Query().Get("win"))
	dur := windowDur[winKey]
	from, to := windowRange(dur)

	hasData, err := h.q.Tenant(tid).HasAnySummary(r.Context())
	if err != nil {
		h.serverError(w, "has-data check", err)
		return
	}
	if !hasData {
		h.renderOnboarding(w, r, tid, winKey)
		return
	}

	services, err := h.q.Tenant(tid).Services(r.Context(), from, to)
	if err != nil {
		h.serverError(w, "services query", err)
		return
	}

	// Casual-check live refresh is opt-in: ?live=1 turns on a meta-refresh and
	// lights the top-bar toggle; the default view is static.
	live := isLive(r)
	shell := h.buildShell(r, "overview", []crumb{{Label: "services"}}, "Service overview", true, winKey)
	shell.Live = live
	shell.LiveHref = liveToggleHref(r, live)

	rows := toServiceRows(services, dur, winKey)
	h.render(w, "overview", overviewData{
		Shell:       shell,
		Frozen:      h.frozenFor(tid),
		KPIs:        overviewKPIs(rows, windowText[winKey]),
		Services:    rows,
		WindowLabel: windowText[winKey],
	})
}

// serveEndpoints renders level 2: the sortable endpoint table for a service.
func (h *Handler) serveEndpoints(w http.ResponseWriter, r *http.Request) {
	tid, ok := h.resolveTenant(w, r)
	if !ok {
		return
	}
	service := r.PathValue("service")
	winKey := normalizeWindow(r.URL.Query().Get("win"))
	dur := windowDur[winKey]
	from, to := windowRange(dur)

	endpoints, err := h.q.Tenant(tid).Endpoints(r.Context(), service, from, to)
	if err != nil {
		h.serverError(w, "endpoints query", err)
		return
	}

	sortKey := normalizeSort(r.URL.Query().Get("sort"))
	rows := toEndpointRows(endpoints, dur)
	sortEndpointRows(rows, sortKey)

	crumbs := []crumb{{Label: "services", Href: withWin("/", winKey)}, {Label: service}}
	h.render(w, "endpoints", endpointsData{
		Shell:     h.buildShell(r, "overview", crumbs, service, true, winKey),
		Service:   service,
		Sort:      sortKey,
		Frozen:    h.frozenFor(tid),
		KPIs:      endpointKPIs(rows),
		Endpoints: rows,
	})
}

// renderOnboarding builds and renders the 4-step onboarding panel from the live
// handshake/summary state, plus the frozen-cardinality warning.
func (h *Handler) renderOnboarding(w http.ResponseWriter, r *http.Request, tid tenant.ID, winKey string) {
	var connected []ServiceOnboarding
	if h.onboarding != nil {
		got, err := h.onboarding(r.Context(), tid.String())
		if err != nil {
			// Onboarding is best-effort UI: log and fall back to "no source
			// connected yet" rather than 500 the whole page.
			h.log.Error("web: onboarding source", slog.Any("err", err))
		} else {
			connected = got
		}
	}
	ob := buildOnboarding(connected, h.frozenFor(tid))
	h.render(w, "onboarding", onboardingPage{
		Shell:     h.buildShell(r, "setup", []crumb{{Label: "setup"}}, "Get started", false, winKey),
		Steps:     onboardingStepViews(ob.Steps),
		Connected: ob.Connected,
		Frozen:    ob.Frozen,
		// This panel is reached only while the tenant has no summary yet, so it is
		// always incomplete: refresh so it self-terminates onto the live overview
		// the moment the first Summary lands.
		Refresh: true,
	})
}

// frozenFor reports the tenant's frozen state through the nil-safe FrozenFunc.
func (h *Handler) frozenFor(tid tenant.ID) bool {
	if h.frozen == nil {
		return false
	}
	return h.frozen(tid.String())
}

// serverError logs a query failure and writes a 500. Handlers pass a short
// context string so the log identifies which query failed.
func (h *Handler) serverError(w http.ResponseWriter, what string, err error) {
	h.log.Error("web: "+what, slog.Any("err", err))
	http.Error(w, "query failed", http.StatusInternalServerError)
}

// render executes the named template into w, logging (not double-writing) on a
// render error since the header is already committed.
func (h *Handler) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
	if err := h.tpl.ExecuteTemplate(w, name, data); err != nil {
		h.log.Error("web: render "+name, slog.Any("err", err))
	}
}
