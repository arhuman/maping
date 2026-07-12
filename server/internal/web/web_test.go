package web

import (
	"context"
	"encoding/json"
	"errors"
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
		{Method: "GET", Route: "/users/:id", Count: 100, ErrorRate: 0.01, P50: 0.01, P95: 0.1, P99: 0.3},
	}}
	srv := newServer(t, Config{Querier: q, Tenant: constTenant})
	code, body := getBody(t, srv.URL+"/services/checkout-api")
	assert.Equal(t, http.StatusOK, code)
	// Method and route render in separate cells now (chip + route span).
	assert.Contains(t, body, "/users/:id")
	assert.Contains(t, body, "method=GET")
	// Default sort is traffic -> its header carries the active arrow.
	assert.Contains(t, body, "TRAFFIC ▾")
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
