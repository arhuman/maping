package web

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arhuman/maping/server/internal/storage"
	"github.com/arhuman/maping/server/internal/tenant"
)

// fakeQuerier is a scriptable Querier for the handler tests: each field feeds
// the matching method, and the err fields force the 500 paths.
type fakeQuerier struct {
	hasData    bool
	hasDataErr error

	services    []storage.ServiceStat
	servicesErr error

	endpoints    []storage.EndpointStat
	endpointsErr error

	detail    storage.EndpointDetail
	detailErr error

	points    []storage.TimePoint
	seriesErr error

	instances    []storage.InstanceStat
	instancesErr error

	versions    []storage.VersionStat
	versionsErr error

	exemplars    []storage.ExemplarRow
	exemplarsErr error

	classLatency    map[string]storage.ClassLatency
	classLatencyErr error

	errorClasses    []storage.ErrorClassStat
	errorClassesErr error

	noStatusReasons    []storage.NoStatusReasonStat
	noStatusReasonsErr error

	downstream    storage.DownstreamStat
	downstreamErr error

	resources    []storage.InstanceResourceStat
	resourcesErr error

	performance    storage.PerformanceStat
	performanceErr error
}

// Tenant binds the fake to a tenant, returning a fakeScopedQuery over the same
// canned data. The tenant is ignored: the tests assert on the canned responses,
// not on tenant threading (that is enforced by the type in storage).
func (f fakeQuerier) Tenant(tenant.ID) ScopedQuery { return fakeScopedQuery{f} }

// fakeScopedQuery is the tenant-bound view of fakeQuerier: each method feeds the
// matching canned field, and the err fields force the 500 paths.
type fakeScopedQuery struct{ f fakeQuerier }

func (s fakeScopedQuery) HasAnySummary(context.Context) (bool, error) {
	return s.f.hasData, s.f.hasDataErr
}
func (s fakeScopedQuery) Services(context.Context, time.Time, time.Time) ([]storage.ServiceStat, error) {
	return s.f.services, s.f.servicesErr
}
func (s fakeScopedQuery) Endpoints(context.Context, string, time.Time, time.Time) ([]storage.EndpointStat, error) {
	return s.f.endpoints, s.f.endpointsErr
}
func (s fakeScopedQuery) EndpointDetail(context.Context, string, string, string, time.Time, time.Time) (storage.EndpointDetail, error) {
	return s.f.detail, s.f.detailErr
}
func (s fakeScopedQuery) SeriesOverTime(context.Context, string, string, string, time.Time, time.Time, time.Duration) ([]storage.TimePoint, error) {
	return s.f.points, s.f.seriesErr
}
func (s fakeScopedQuery) InstancesForEndpoint(context.Context, string, string, string, time.Time, time.Time) ([]storage.InstanceStat, error) {
	return s.f.instances, s.f.instancesErr
}
func (s fakeScopedQuery) VersionsForEndpoint(context.Context, string, string, string, time.Time, time.Time) ([]storage.VersionStat, error) {
	return s.f.versions, s.f.versionsErr
}
func (s fakeScopedQuery) ExemplarsForEndpoint(context.Context, string, string, string, time.Time, time.Time) ([]storage.ExemplarRow, error) {
	return s.f.exemplars, s.f.exemplarsErr
}
func (s fakeScopedQuery) LatencyByStatusClass(context.Context, string, string, string, time.Time, time.Time) (map[string]storage.ClassLatency, error) {
	return s.f.classLatency, s.f.classLatencyErr
}
func (s fakeScopedQuery) ErrorClassesForEndpoint(context.Context, string, string, string, time.Time, time.Time) ([]storage.ErrorClassStat, error) {
	return s.f.errorClasses, s.f.errorClassesErr
}
func (s fakeScopedQuery) NoStatusReasonsForEndpoint(context.Context, string, string, string, time.Time, time.Time) ([]storage.NoStatusReasonStat, error) {
	return s.f.noStatusReasons, s.f.noStatusReasonsErr
}
func (s fakeScopedQuery) DownstreamForEndpoint(context.Context, string, string, string, time.Time, time.Time) (storage.DownstreamStat, error) {
	return s.f.downstream, s.f.downstreamErr
}
func (s fakeScopedQuery) InstanceResourcesForService(context.Context, string, time.Time, time.Time) ([]storage.InstanceResourceStat, error) {
	return s.f.resources, s.f.resourcesErr
}
func (s fakeScopedQuery) PerformanceStats(context.Context, time.Time, time.Time) (storage.PerformanceStat, error) {
	return s.f.performance, s.f.performanceErr
}

// constTenant is the Part-1 tenant resolver: always resolves the dev tenant.
func constTenant(_ *http.Request) (tenant.ID, bool) { return tenant.MustParse("dev-tenant"), true }

func newServer(t *testing.T, cfg Config) *httptest.Server {
	t.Helper()
	if cfg.Tenant == nil {
		cfg.Tenant = constTenant
	}
	h, err := NewHandler(cfg)
	require.NoError(t, err)
	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func getBody(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(b)
}

func TestNewHandlerValidation(t *testing.T) {
	_, err := NewHandler(Config{Tenant: constTenant})
	require.Error(t, err, "nil Querier must error")
	_, err = NewHandler(Config{Querier: fakeQuerier{}})
	require.Error(t, err, "nil Tenant must error")
}

func TestAccountHrefLinksUserBlock(t *testing.T) {
	srv := newServer(t, Config{Querier: fakeQuerier{}, Tenant: constTenant, AccountHref: "/account"})
	code, body := getBody(t, srv.URL+"/")
	require.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, `<a href="/account" class="userbox">`, "user block links to the account page when AccountHref is set")

	plain := newServer(t, Config{Querier: fakeQuerier{}, Tenant: constTenant})
	_, pbody := getBody(t, plain.URL+"/")
	assert.NotContains(t, pbody, `href="/account"`, "no account link without AccountHref")
}

func TestRenderShellPage(t *testing.T) {
	h, err := NewHandler(Config{Querier: fakeQuerier{}, Tenant: constTenant, AccountHref: "/account"})
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	h.RenderShellPage(rec, httptest.NewRequest(http.MethodGet, "/account", nil),
		"Account", template.HTML(`<div class="panel">HELLO-CONTENT</div>`))

	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "HELLO-CONTENT", "content renders inside the shell")
	assert.Contains(t, body, `class="aside"`, "sidebar chrome renders")
	assert.Contains(t, body, "mAPI-ng — Account", "browser title set from page title")
	assert.Contains(t, body, `<a href="/account" class="userbox">`, "user block links to account inside the shell page too")
}

func TestCopyJSAssetServed(t *testing.T) {
	srv := newServer(t, Config{Querier: fakeQuerier{}, Tenant: constTenant})

	resp, err := http.Get(srv.URL + "/assets/copy.js")
	require.NoError(t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/javascript; charset=utf-8", resp.Header.Get("Content-Type"))
	// The helper is the delegated copy-to-clipboard script.
	assert.Contains(t, string(b), "data-copy")
	assert.Contains(t, string(b), "navigator.clipboard")
	// CSP pins the JS budget: only self-hosted scripts run.
	assert.Contains(t, resp.Header.Get("Content-Security-Policy"), "script-src 'self'")
}

func TestHTMLPagesCarryCSP(t *testing.T) {
	srv := newServer(t, Config{Querier: fakeQuerier{hasData: true}, Tenant: constTenant})
	resp, err := http.Get(srv.URL + "/")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Contains(t, resp.Header.Get("Content-Security-Policy"), "script-src 'self'")
}

func TestOverviewRendersServices(t *testing.T) {
	q := fakeQuerier{hasData: true, services: []storage.ServiceStat{
		{Service: "checkout-api", Count: 3600, ErrorRate: 0.10, P50: 0.01, P95: 0.2, P99: 0.5},
	}}
	srv := newServer(t, Config{Querier: q, Tenant: constTenant})

	code, body := getBody(t, srv.URL+"/")
	assert.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, "checkout-api")
	assert.Contains(t, body, "/services/checkout-api")
	// Error rate 10% >= 5% threshold -> error-coloured cell class.
	assert.Contains(t, body, "c-err")
}

func TestOverviewAliasDashboard(t *testing.T) {
	srv := newServer(t, Config{Querier: fakeQuerier{hasData: true}, Tenant: constTenant})
	code, _ := getBody(t, srv.URL+"/dashboard")
	assert.Equal(t, http.StatusOK, code)
}

func TestOverviewShowsOnboardingWhenNoData(t *testing.T) {
	q := fakeQuerier{hasData: false}
	onboard := func(context.Context, string) ([]ServiceOnboarding, error) {
		return []ServiceOnboarding{{Service: "checkout", Instance: "pod-a"}}, nil
	}
	srv := newServer(t, Config{Querier: q, Tenant: constTenant, Onboarding: onboard})

	code, body := getBody(t, srv.URL+"/")
	assert.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, "One env var")
	assert.Contains(t, body, "Ingest key valid")
	assert.Contains(t, body, "Service connected")
	// The connected source is listed.
	assert.Contains(t, body, "pod-a")
}

func TestOnboardingFrozenWarning(t *testing.T) {
	q := fakeQuerier{hasData: false}
	srv := newServer(t, Config{
		Querier: q, Tenant: constTenant,
		Frozen: func(string) bool { return true },
	})
	code, body := getBody(t, srv.URL+"/")
	assert.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, "Cardinality frozen")
}

func TestOnboardingFrameworkSelector(t *testing.T) {
	srv := newServer(t, Config{Querier: fakeQuerier{hasData: false}, Tenant: constTenant})

	// Default view: every adapter's wire-up snippet is rendered, all five tabs are
	// present, and gin is the checked default.
	code, body := getBody(t, srv.URL+"/")
	assert.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, "mapinggin.MiddlewareWithRecorder")
	assert.Contains(t, body, "mapinghttp.MiddlewareWithRecorder")
	assert.Contains(t, body, "mapingecho.MiddlewareWithRecorder")
	assert.Contains(t, body, "mapingchi.MiddlewareWithRecorder")
	assert.Contains(t, body, "mapingbeego.FilterWithRecorder")
	assert.Contains(t, body, `>net/http</label>`)
	assert.Contains(t, body, `>beego</label>`)
	assert.Contains(t, body, `id="fw-gin" checked`)

	// ?fw= pre-checks the matching radio (deep-link into a framework).
	_, chiBody := getBody(t, srv.URL+"/?fw=chi")
	assert.Contains(t, chiBody, `id="fw-chi" checked`)
	assert.NotContains(t, chiBody, `id="fw-gin" checked`)

	// An unknown framework falls back to gin so the CSS switcher never hides every
	// snippet.
	_, badBody := getBody(t, srv.URL+"/?fw=bogus")
	assert.Contains(t, badBody, `id="fw-gin" checked`)
}

func TestOverviewQueryError(t *testing.T) {
	srv := newServer(t, Config{
		Querier: fakeQuerier{hasData: true, servicesErr: errors.New("boom")},
		Tenant:  constTenant,
	})
	code, _ := getBody(t, srv.URL+"/")
	assert.Equal(t, http.StatusInternalServerError, code)
}

func TestOverviewHasDataError(t *testing.T) {
	srv := newServer(t, Config{
		Querier: fakeQuerier{hasDataErr: errors.New("boom")},
		Tenant:  constTenant,
	})
	code, _ := getBody(t, srv.URL+"/")
	assert.Equal(t, http.StatusInternalServerError, code)
}

func TestUnauthorizedWhenTenantUnresolved(t *testing.T) {
	srv := newServer(t, Config{
		Querier: fakeQuerier{},
		Tenant:  func(*http.Request) (tenant.ID, bool) { return tenant.ID{}, false },
	})
	code, _ := getBody(t, srv.URL+"/")
	assert.Equal(t, http.StatusUnauthorized, code)
}

func TestUnknownPathIs404(t *testing.T) {
	srv := newServer(t, Config{Querier: fakeQuerier{hasData: true}, Tenant: constTenant})
	code, _ := getBody(t, srv.URL+"/nope")
	assert.Equal(t, http.StatusNotFound, code)
}

func TestEndpointsTable(t *testing.T) {
	q := fakeQuerier{endpoints: []storage.EndpointStat{
		{Method: "GET", Route: "/users/:id", Count: 100, ErrorRate: 0.01, P50: 0.01, P95: 0.1, P99: 0.3, ReqBytesAvg: 128, RespBytesAvg: 2048},
	}}
	srv := newServer(t, Config{Querier: q, Tenant: constTenant})
	code, body := getBody(t, srv.URL+"/services/checkout-api")
	assert.Equal(t, http.StatusOK, code)
	// Method and route render in separate cells now (chip + route span).
	assert.Contains(t, body, "/users/:id")
	assert.Contains(t, body, "method=GET")
	// Default sort is traffic -> its header carries the active arrow.
	assert.Contains(t, body, "TRAFFIC ▾")
	// Byte-average columns render human-readably (128 B, 2 KB).
	assert.Contains(t, body, "AVG REQ")
	assert.Contains(t, body, "AVG RESP")
	assert.Contains(t, body, "128 B")
	assert.Contains(t, body, "2 KB")
}

func TestEndpointsSortAllowlist(t *testing.T) {
	q := fakeQuerier{endpoints: []storage.EndpointStat{
		{Method: "GET", Route: "/a", Count: 10, ErrorRate: 0.01, P99: 0.9},
		{Method: "GET", Route: "/b", Count: 100, ErrorRate: 0.50, P99: 0.1},
	}}
	srv := newServer(t, Config{Querier: q, Tenant: constTenant})

	// Sort by error: /b (0.50) must appear before /a (0.01). Match each row's
	// visible route text (>/b<, >/a<) so the ordering assertion keys on row
	// order, not shared chrome (the href query value is percent-escaped).
	_, byErr := getBody(t, srv.URL+"/services/svc?sort=error")
	assert.Less(t, strings.Index(byErr, ">/b<"), strings.Index(byErr, ">/a<"))
	assert.Contains(t, byErr, "ERROR % ▾")

	// Unknown sort falls back to traffic (/b count 100 before /a count 10).
	_, byBad := getBody(t, srv.URL+"/services/svc?sort=;DROP")
	assert.Contains(t, byBad, "TRAFFIC ▾")
	assert.Less(t, strings.Index(byBad, ">/b<"), strings.Index(byBad, ">/a<"))
}

func TestEndpointDetailPage(t *testing.T) {
	q := fakeQuerier{detail: storage.EndpointDetail{
		Count: 100, ErrorRate: 0.20, P50: 0.01, P95: 0.2, P99: 0.5,
		StatusClasses: []storage.StatusClassCount{
			{Class: "2xx", Count: 80}, {Class: "4xx", Count: 15}, {Class: "5xx", Count: 5},
		},
		StatusCodes: map[uint32]uint64{200: 80, 404: 15, 500: 5},
		Histogram:   []storage.HistogramBar{{LatencySeconds: 0.01, Count: 100}},
	}}
	srv := newServer(t, Config{Querier: q, Tenant: constTenant})

	code, body := getBody(t, srv.URL+"/services/svc/endpoint?method=GET&route=/x")
	assert.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, "Status breakdown")
	assert.Contains(t, body, "20.0%") // headline error rate (design pct format)
	assert.Contains(t, body, "EXACT CODES")
	assert.Contains(t, body, "c-err") // 20% >= threshold -> error colour class
}

// TestEndpointDetailInstancesPanel asserts the instance-outlier panel renders
// each replica server-side with its own RED metrics and byte averages, so a bad
// replica (pod-b: 75% errors) is visible without any client JS.
func TestEndpointDetailInstancesPanel(t *testing.T) {
	q := fakeQuerier{
		detail: storage.EndpointDetail{Count: 140, ErrorRate: 0.05},
		instances: []storage.InstanceStat{
			{Instance: "pod-a", Count: 100, ErrorRate: 0.01, P50: 0.01, P95: 0.2, P99: 0.5, ReqBytesAvg: 128, RespBytesAvg: 2048},
			{Instance: "pod-b", Count: 40, ErrorRate: 0.75, P50: 0.02, P95: 0.9, P99: 1.5, ReqBytesAvg: 130, RespBytesAvg: 4096},
		},
	}
	srv := newServer(t, Config{Querier: q, Tenant: constTenant})

	code, body := getBody(t, srv.URL+"/services/svc/endpoint?method=GET&route=/x")
	assert.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, "Instances")
	// Both replicas render, ordered by instance (pod-a before pod-b).
	assert.Contains(t, body, "pod-a")
	assert.Contains(t, body, "pod-b")
	assert.Less(t, strings.Index(body, "pod-a"), strings.Index(body, "pod-b"))
	// The outlier's elevated error rate is rendered (design pct format).
	assert.Contains(t, body, "75.0%")
	// Byte averages render human-readably (2 KB, 4 KB).
	assert.Contains(t, body, "2 KB")
	assert.Contains(t, body, "4 KB")
}

// TestEndpointDetailVersionsPanel asserts the deploy-version panel renders each
// version server-side with its own RED metrics, so a regressing release
// (v1.1.0: 40% errors) is visible without any client JS, and that the DEBUG
// CONTEXT line surfaces the dominant deploy_version (the highest-traffic one).
func TestEndpointDetailVersionsPanel(t *testing.T) {
	q := fakeQuerier{
		detail: storage.EndpointDetail{Count: 300, ErrorRate: 0.10},
		versions: []storage.VersionStat{
			{Version: "v1.0.0", Count: 200, ErrorRate: 0.01, P50: 0.01, P95: 0.2, P99: 0.5},
			{Version: "v1.1.0", Count: 100, ErrorRate: 0.40, P50: 0.02, P95: 0.9, P99: 1.5},
		},
	}
	srv := newServer(t, Config{Querier: q, Tenant: constTenant})

	code, body := getBody(t, srv.URL+"/services/svc/endpoint?method=GET&route=/x")
	assert.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, "Versions")
	// Both versions render, in storage order (v1.0.0 before v1.1.0).
	assert.Contains(t, body, "v1.0.0")
	assert.Contains(t, body, "v1.1.0")
	assert.Less(t, strings.Index(body, "v1.0.0"), strings.Index(body, "v1.1.0"))
	// The regressing release's elevated error rate renders (design pct format).
	assert.Contains(t, body, "40.0%")
	// The DEBUG CONTEXT line surfaces the dominant version (v1.0.0, most traffic).
	debug := body[strings.Index(body, "DEBUG CONTEXT"):]
	assert.Contains(t, debug, "version v1.0.0")
}

// TestEndpointDetailExemplarsPanel asserts the exemplars panel renders each
// captured request server-side (time, status, latency, ids), that a non-empty
// trace/request id is shown and carried in full for the copy button, and that an
// exemplar with empty ids renders the em-dash placeholder — all with no client JS.
func TestEndpointDetailExemplarsPanel(t *testing.T) {
	at := time.Date(2026, 7, 13, 14, 30, 15, 0, time.UTC)
	q := fakeQuerier{
		detail: storage.EndpointDetail{Count: 100, ErrorRate: 0.05},
		exemplars: []storage.ExemplarRow{
			// A slow error with full trace/request ids.
			{At: at, DurationNs: 1_500_000_000, StatusCode: 500, TraceID: "trace-abcdef0123456789", SpanID: "span-1", RequestID: "req-abcdef0123456789"},
			// A fast success with no ids captured -> em-dash cells.
			{At: at.Add(-time.Minute), DurationNs: 12_000_000, StatusCode: 200, TraceID: "", SpanID: "", RequestID: ""},
		},
	}
	srv := newServer(t, Config{Querier: q, Tenant: constTenant})

	code, body := getBody(t, srv.URL+"/services/svc/endpoint?method=GET&route=/x")
	assert.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, "Exemplars")
	assert.Contains(t, body, "jump from a spike to a real request")
	// Scope assertions to the exemplars panel so they key on the new panel, not
	// on the headline or other panels.
	ex := body[strings.Index(body, "Exemplars"):]
	// The compact UTC time renders.
	assert.Contains(t, ex, "14:30:15")
	// The full trace/request ids are present (kept whole for the copy button).
	assert.Contains(t, ex, "trace-abcdef0123456789")
	assert.Contains(t, ex, "req-abcdef0123456789")
	// The slow exemplar's latency renders through the shared formatter (1.5 s).
	assert.Contains(t, ex, "1.50 s")
	// The copy buttons reuse the existing data-copy mechanism (no new JS).
	assert.Contains(t, ex, "data-copy=\"mp-ex-tr-0\"")
	assert.Contains(t, ex, "data-copy=\"mp-ex-rq-0\"")
	// The empty-id exemplar renders the em-dash placeholder.
	assert.Contains(t, ex, "—")
}

// TestEndpointDetailExemplarsEmpty asserts the empty-state row renders when no
// exemplars are captured in the window.
func TestEndpointDetailExemplarsEmpty(t *testing.T) {
	q := fakeQuerier{detail: storage.EndpointDetail{Count: 100, ErrorRate: 0.05}}
	srv := newServer(t, Config{Querier: q, Tenant: constTenant})

	code, body := getBody(t, srv.URL+"/services/svc/endpoint?method=GET&route=/x")
	assert.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, "No exemplars captured in this window.")
}

// TestEndpointDetailStatusClassSplit asserts the success-vs-error latency split
// renders one row per traffic-carrying status class (with its per-class request
// count) server-side, and omits zero-traffic classes.
func TestEndpointDetailStatusClassSplit(t *testing.T) {
	q := fakeQuerier{
		detail: storage.EndpointDetail{Count: 100, ErrorRate: 0.20},
		classLatency: map[string]storage.ClassLatency{
			"STATUS_CLASS_2XX": {Count: 80, P50: 0.01, P95: 0.05, P99: 0.1},
			"STATUS_CLASS_5XX": {Count: 20, P50: 0.5, P95: 1.2, P99: 2.0},
			// 3xx/4xx/no_status have no traffic and must be omitted.
			"STATUS_CLASS_3XX": {},
		},
	}
	srv := newServer(t, Config{Querier: q, Tenant: constTenant})

	code, body := getBody(t, srv.URL+"/services/svc/endpoint?method=GET&route=/x")
	assert.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, "Latency by status class")
	// The split panel renders a row per class with traffic; use a distinctive
	// per-class latency (2.0 s p99 on 5xx) to key on the split, not the headline.
	assert.Contains(t, body, "2.00 s")
	// The 5xx row carries its own request count.
	split := body[strings.Index(body, "Latency by status class"):]
	assert.Contains(t, split, ">5xx<")
	assert.Contains(t, split, ">2xx<")
	// A zero-traffic class (3xx) is omitted from the split.
	assert.NotContains(t, split, ">3xx<")
}

func TestSeriesJSON(t *testing.T) {
	ts := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	q := fakeQuerier{points: []storage.TimePoint{
		{TS: ts, Count: 100, ErrorRate: 0.05, P50: 0.01, P95: 0.2, P99: 0.5},
	}}
	srv := newServer(t, Config{Querier: q, Tenant: constTenant})

	resp, err := http.Get(srv.URL + "/api/series?service=svc&method=GET&route=/x")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var out []seriesPoint
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.Len(t, out, 1)
	assert.Equal(t, ts.Unix(), out[0].TS)
	assert.InDelta(t, 0.2, out[0].P95, 1e-9)
}

func TestSeriesQueryError(t *testing.T) {
	srv := newServer(t, Config{
		Querier: fakeQuerier{seriesErr: errors.New("boom")},
		Tenant:  constTenant,
	})
	code, _ := getBody(t, srv.URL+"/api/series")
	assert.Equal(t, http.StatusInternalServerError, code)
}

func TestHistogramJSON(t *testing.T) {
	q := fakeQuerier{detail: storage.EndpointDetail{
		Histogram: []storage.HistogramBar{
			{LatencySeconds: 0.01, Count: 10}, {LatencySeconds: 0.05, Count: 3},
		},
	}}
	srv := newServer(t, Config{Querier: q, Tenant: constTenant})

	resp, err := http.Get(srv.URL + "/api/histogram?service=svc&method=GET&route=/x")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var out []histogramBar
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.Len(t, out, 2)
	assert.InDelta(t, 0.01, out[0].Latency, 1e-9)
	assert.Equal(t, uint64(10), out[0].Count)
}

func TestHistogramQueryError(t *testing.T) {
	srv := newServer(t, Config{
		Querier: fakeQuerier{detailErr: errors.New("boom")},
		Tenant:  constTenant,
	})
	code, _ := getBody(t, srv.URL+"/api/histogram")
	assert.Equal(t, http.StatusInternalServerError, code)
}

func TestInstancesJSON(t *testing.T) {
	q := fakeQuerier{instances: []storage.InstanceStat{
		{Instance: "pod-a", Count: 100, ErrorRate: 0.01, P50: 0.01, P95: 0.2, P99: 0.5, ReqBytesAvg: 128, RespBytesAvg: 2048},
		{Instance: "pod-b", Count: 40, ErrorRate: 0.75, P50: 0.02, P95: 0.9, P99: 1.5, ReqBytesAvg: 130, RespBytesAvg: 4096},
	}}
	srv := newServer(t, Config{Querier: q, Tenant: constTenant})

	resp, err := http.Get(srv.URL + "/api/instances?service=svc&method=GET&route=/x")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var out []instanceRow
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.Len(t, out, 2)
	// The outlier replica (pod-b) is surfaced with its own elevated error rate.
	assert.Equal(t, "pod-b", out[1].Instance)
	assert.InDelta(t, 0.75, out[1].ErrorRate, 1e-9)
	assert.InDelta(t, 4096, out[1].RespBytesAvg, 1e-9)
}

func TestInstancesQueryError(t *testing.T) {
	srv := newServer(t, Config{
		Querier: fakeQuerier{instancesErr: errors.New("boom")},
		Tenant:  constTenant,
	})
	code, _ := getBody(t, srv.URL+"/api/instances")
	assert.Equal(t, http.StatusInternalServerError, code)
}
