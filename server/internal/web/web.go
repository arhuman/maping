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
	"context"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"time"

	"github.com/arhuman/maping/server/internal/storage"
	"github.com/arhuman/maping/server/internal/tenant"
)

// window is the fixed dashboard lookback. The 3-level view is a live RED pane,
// not a range picker (CONTEXT: fixed, non-configurable dashboard), so a single
// window keeps the UI simple and the tier selection deterministic.
const window = time.Hour

// seriesStep is the time-series bucket width for the detail chart.
const seriesStep = time.Minute

// Querier is the read side the web layer depends on. It exposes no un-scoped
// query: the only way to reach the data plane is Tenant(tenant), which returns a
// tenant-bound ScopedQuery. This makes a cross-tenant read unrepresentable —
// isolation is a type property, not caller discipline. storage.TenantQuery
// structurally satisfies ScopedQuery; a fake satisfies Querier in tests, so the
// web layer never imports a live ClickHouse connection.
type Querier interface {
	Tenant(id tenant.ID) ScopedQuery
}

// ScopedQuery is the tenant-bound aggregate surface the dashboard reads. Every
// method is already scoped to the tenant the handle was created for, so no
// call site passes a tenant string.
type ScopedQuery interface {
	SeriesOverTime(ctx context.Context, service, method, route string, from, to time.Time, step time.Duration) ([]storage.TimePoint, error)
	Services(ctx context.Context, from, to time.Time) ([]storage.ServiceStat, error)
	Endpoints(ctx context.Context, service string, from, to time.Time) ([]storage.EndpointStat, error)
	EndpointDetail(ctx context.Context, service, method, route string, from, to time.Time) (storage.EndpointDetail, error)
	InstancesForEndpoint(ctx context.Context, service, method, route string, from, to time.Time) ([]storage.InstanceStat, error)
	HasAnySummary(ctx context.Context) (bool, error)
}

// TenantResolver resolves the active tenant for a request. Part 1 supplies a
// constant-tenant func; Part 2 (auth) supplies the authenticated org. ok=false
// means no tenant could be resolved (unauthenticated), which the handlers turn
// into a 401 — but Part 1's constant func always returns ok=true.
type TenantResolver func(r *http.Request) (id tenant.ID, ok bool)

// ServiceOnboarding mirrors control.ServiceOnboarding without importing control
// (web sits downstream of the control plane and must not depend on it). main
// adapts the control type into this at wiring time.
type ServiceOnboarding struct {
	Service     string
	Instance    string
	HandshakeAt time.Time
}

// OnboardingSource returns the connected services for a tenant, driving the
// onboarding panel. Nil-safe: when unset (no control plane), the panel shows the
// key-valid step and nothing beyond it rather than inventing data.
type OnboardingSource func(ctx context.Context, tenant string) ([]ServiceOnboarding, error)

// FrozenFunc reports whether a tenant's cardinality is frozen on this node, so
// the onboarding/dashboard can surface the guardrail warning loudly (CONTEXT
// Guardrails). Nil-safe: unset means "no frozen signal available", not "false".
type FrozenFunc func(tenant string) bool

// KeyInfo is a listed ingest key for the Setup keys panel. It mirrors
// control.KeyInfo so the web layer never imports the control plane; main adapts
// between the two. It never carries the secret — only the display last-4 and
// lifecycle timestamps.
type KeyInfo struct {
	ID        string
	Label     string
	Last4     string
	CreatedAt time.Time
	RevokedAt *time.Time // nil while the key is active
}

// KeyAdmin is the self-serve key surface the Setup page drives: issue (returns
// the full one-time token, origin already wrapped by main), list, and revoke.
// Nil-safe: when unset (dev/no-control-plane) the keys panel is hidden and the
// key POST routes 404, so the dashboard still renders without a control plane.
type KeyAdmin interface {
	IssueKey(ctx context.Context, orgID, label string) (token string, err error)
	ListKeys(ctx context.Context, orgID string) ([]KeyInfo, error)
	RevokeKey(ctx context.Context, orgID, keyID string) error
}

// Handler serves the 3-level dashboard, the onboarding panel, and the JSON data
// endpoints. Every dependency beyond the querier is injected and nil-safe.
type Handler struct {
	q          Querier
	tenant     TenantResolver
	onboarding OnboardingSource // may be nil (no control plane).
	frozen     FrozenFunc       // may be nil (no guardrail signal).
	keys       KeyAdmin         // may be nil (no control plane): hides the keys panel.
	csrf       *csrf            // nil when keys is nil; guards the key POSTs.
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
	// CSRFKey signs the Setup form CSRF tokens (HMAC). Required (>= 1 byte) when
	// KeyAdmin is set; ignored otherwise. main passes the session-signing key.
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
	if cfg.KeyAdmin != nil && len(cfg.CSRFKey) == 0 {
		return nil, fmt.Errorf("web.NewHandler: KeyAdmin set without a CSRFKey")
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
		Nav:          buildNav(activeNav),
		Crumbs:       crumbs,
		PageTitle:    title,
		ShowControls: showControls,
		Windows:      buildWindows(r, winKey),
		WindowKey:    winKey,
		FlushLabel:   "flush 10s",
	}
}

// servePerformance renders the static performance/architecture page. It carries
// no live data — the figures are the platform's design characteristics.
func (h *Handler) servePerformance(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.resolveTenant(w, r); !ok {
		return
	}
	winKey := normalizeWindow(r.URL.Query().Get("win"))
	h.render(w, "performance", performancePage{
		Shell: h.buildShell(r, "performance", []crumb{{Label: "performance"}}, "Performance", false, winKey),
	})
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

	rows := toServiceRows(services, dur)
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

	crumbs := []crumb{{Label: "services", Href: "/"}, {Label: service}}
	h.render(w, "endpoints", endpointsData{
		Shell:     h.buildShell(r, "overview", crumbs, service, true, winKey),
		Service:   service,
		Sort:      sortKey,
		Frozen:    h.frozenFor(tid),
		KPIs:      endpointKPIs(rows),
		Endpoints: rows,
	})
}

// serveEndpointDetail renders level 3: the detail page (time-series chart via
// /api/series, latency histogram via /api/histogram, and the class breakdown
// beside the headline error rate).
func (h *Handler) serveEndpointDetail(w http.ResponseWriter, r *http.Request) {
	tid, ok := h.resolveTenant(w, r)
	if !ok {
		return
	}
	service := r.PathValue("service")
	method := r.URL.Query().Get("method")
	route := r.URL.Query().Get("route")
	winKey := normalizeWindow(r.URL.Query().Get("win"))
	dur := windowDur[winKey]
	from, to := windowRange(dur)

	tq := h.q.Tenant(tid)
	detail, err := tq.EndpointDetail(r.Context(), service, method, route, from, to)
	if err != nil {
		h.serverError(w, "endpoint detail query", err)
		return
	}

	// The time-series chart is secondary to the detail headline: a series-query
	// failure logs and renders an empty chart rather than 500-ing the page.
	points, err := tq.SeriesOverTime(r.Context(), service, method, route, from, to, seriesStep)
	if err != nil {
		h.log.Error("web: detail series", slog.Any("err", err))
		points = nil
	}

	dv := toDetailView(detail)
	crumbs := []crumb{{Label: "services", Href: "/"}, {Label: service, Href: "/services/" + service}, {Label: method + " " + route}}
	h.render(w, "detail", detailData{
		Shell:      h.buildShell(r, "overview", crumbs, method+" "+route, true, winKey),
		Service:    service,
		Method:     method,
		Route:      route,
		Detail:     dv,
		Stats:      detailStats(dv, dur.Seconds()),
		StatusBars: statusBarsFor(dv),
		Debug:      buildDebugContext(service, method, route, from, to, dv),
		TSChart:    timeSeriesSVG(points, seriesStep),
		HistChart:  histogramSVG(detail.Histogram, detail.P50, detail.P95, detail.P99),
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
