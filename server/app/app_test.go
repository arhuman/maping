package app

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arhuman/maping/server/internal/control"
	"github.com/arhuman/maping/server/internal/ingest"
	"github.com/arhuman/maping/server/internal/storage"
	"github.com/arhuman/maping/server/internal/tenant"
	"github.com/arhuman/maping/server/internal/web"
)

// testLogger returns a logger that discards output, for wiring tests.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// nopQuerier is a zero-behavior web.Querier for wiring tests. Tenant returns a
// nopScopedQuery, so it satisfies the tenant-scoped read surface.
type nopQuerier struct{}

func (nopQuerier) Tenant(tenant.ID) web.ScopedQuery { return nopScopedQuery{} }

// nopScopedQuery is the zero-behavior tenant-bound read surface for wiring tests.
type nopScopedQuery struct{}

func (nopScopedQuery) SeriesOverTime(context.Context, string, string, string, time.Time, time.Time, time.Duration) ([]storage.TimePoint, error) {
	return nil, nil
}
func (nopScopedQuery) Services(context.Context, time.Time, time.Time) ([]storage.ServiceStat, error) {
	return nil, nil
}
func (nopScopedQuery) Endpoints(context.Context, string, time.Time, time.Time) ([]storage.EndpointStat, error) {
	return nil, nil
}
func (nopScopedQuery) EndpointDetail(context.Context, string, string, string, time.Time, time.Time) (storage.EndpointDetail, error) {
	return storage.EndpointDetail{}, nil
}
func (nopScopedQuery) InstancesForEndpoint(context.Context, string, string, string, time.Time, time.Time) ([]storage.InstanceStat, error) {
	return nil, nil
}
func (nopScopedQuery) VersionsForEndpoint(context.Context, string, string, string, time.Time, time.Time) ([]storage.VersionStat, error) {
	return nil, nil
}
func (nopScopedQuery) ExemplarsForEndpoint(context.Context, string, string, string, time.Time, time.Time) ([]storage.ExemplarRow, error) {
	return nil, nil
}
func (nopScopedQuery) LatencyByStatusClass(context.Context, string, string, string, time.Time, time.Time) (map[string]storage.ClassLatency, error) {
	return nil, nil
}
func (nopScopedQuery) ErrorClassesForEndpoint(context.Context, string, string, string, time.Time, time.Time) ([]storage.ErrorClassStat, error) {
	return nil, nil
}
func (nopScopedQuery) NoStatusReasonsForEndpoint(context.Context, string, string, string, time.Time, time.Time) ([]storage.NoStatusReasonStat, error) {
	return nil, nil
}
func (nopScopedQuery) DownstreamForEndpoint(context.Context, string, string, string, time.Time, time.Time) (storage.DownstreamStat, error) {
	return storage.DownstreamStat{}, nil
}
func (nopScopedQuery) InstanceResourcesForService(context.Context, string, time.Time, time.Time) ([]storage.InstanceResourceStat, error) {
	return nil, nil
}
func (nopScopedQuery) PerformanceStats(context.Context, time.Time, time.Time) (storage.PerformanceStat, error) {
	return storage.PerformanceStat{}, nil
}
func (nopScopedQuery) HasAnySummary(context.Context) (bool, error) { return false, nil }

// fakeControlPlane is a scriptable controlPlane for the dashboard-wiring tests:
// it records the issue label / revoked id and returns the scripted key list,
// secret, and onboarding state — no Postgres needed.
type fakeControlPlane struct {
	issueSecret string
	issueErr    error
	issuedLabel string
	keys        []control.KeyInfo
	listErr     error
	revokedID   string
	onboarding  []control.ServiceOnboarding
	onboardErr  error
}

func (f *fakeControlPlane) IssueKey(_ context.Context, _, label string) (string, error) {
	f.issuedLabel = label
	return f.issueSecret, f.issueErr
}
func (f *fakeControlPlane) ListKeys(context.Context, string) ([]control.KeyInfo, error) {
	return f.keys, f.listErr
}
func (f *fakeControlPlane) RevokeKey(_ context.Context, _, id string) error {
	f.revokedID = id
	return nil
}
func (f *fakeControlPlane) OnboardingState(context.Context, string) ([]control.ServiceOnboarding, error) {
	return f.onboarding, f.onboardErr
}

// fakeMemberStore satisfies auth.MemberStore for the buildAuth tests.
type fakeMemberStore struct{}

func (fakeMemberStore) UpsertMemberFromOIDC(context.Context, string, string) (string, string, string, bool, error) {
	return "org", "mem", "admin", false, nil
}
func (fakeMemberStore) DevOrgAdmin(context.Context, string) (string, string, error) {
	return "org", "mem", nil
}
func (fakeMemberStore) IssueKey(context.Context, string, string) (string, error) {
	return "secret", nil
}

// fakeSink is a no-op ingest.RowSink for the assembleMux wiring tests.
type fakeSink struct{}

func (fakeSink) Enqueue(storage.Row) error { return nil }

// newIngestHandler builds a real ingest handler over fakes so assembleMux can
// mount it without a live writer.
func newIngestHandler(t *testing.T) *ingest.Handler {
	t.Helper()
	return ingest.NewHandler(
		ingest.NewStaticKeyResolver(map[string]string{"dev-key": "dev-tenant"}),
		fakeSink{}, testLogger())
}

// getNoRedirect issues a GET that does not follow redirects, returning the status
// code and the Location header (empty when absent).
func getNoRedirect(t *testing.T, url string) (int, string) {
	t.Helper()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(url) //nolint:noctx // test-only helper, no request context needed.
	require.NoError(t, err)
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, resp.Header.Get("Location")
}

// TestAssembleMuxDevModeWiring asserts the no-control-plane wiring: the dashboard
// is open, and healthz + the ingest Connect path are mounted. This is the boot
// wiring that build() produces, exercised without a live ClickHouse/Postgres.
func TestAssembleMuxDevModeWiring(t *testing.T) {
	mux, ready, cancelBg, err := assembleMux(builtDeps{
		querier:       nopQuerier{},
		ingestHandler: newIngestHandler(t),
		constTenant:   "dev-tenant",
	}, options{}, testLogger())
	require.NoError(t, err)
	defer cancelBg()
	require.True(t, ready.Load())

	srv := httptest.NewServer(mux)
	defer srv.Close()

	// healthz reflects the readiness flag: 200 while serving, 503 once flipped.
	code, _ := getNoRedirect(t, srv.URL+"/healthz")
	assert.Equal(t, http.StatusOK, code)
	ready.Store(false)
	code, _ = getNoRedirect(t, srv.URL+"/healthz")
	assert.Equal(t, http.StatusServiceUnavailable, code)

	// The ingest Connect path is mounted (a bare GET is handled, never a mux 404).
	code, _ = getNoRedirect(t, srv.URL+"/maping.v1.IngestService/Upload")
	assert.NotEqual(t, http.StatusNotFound, code)

	// Dev mode has no auth: "/" serves the dashboard directly, not a login redirect.
	code, loc := getNoRedirect(t, srv.URL+"/")
	assert.Equal(t, http.StatusOK, code)
	assert.Empty(t, loc)
}

// TestAssembleMuxAuthGating asserts that once a control plane (member store +
// session key) is present, the dashboard is auth-gated: an anonymous "/" redirects
// to /login. This is the control-plane-dependent routing decision the split makes
// assertable without infra.
func TestAssembleMuxAuthGating(t *testing.T) {
	mux, _, cancelBg, err := assembleMux(builtDeps{
		querier:       nopQuerier{},
		ingestHandler: newIngestHandler(t),
		cp:            &fakeControlPlane{},
		memberStore:   fakeMemberStore{},
		constTenant:   "dev-tenant",
		baseURL:       "http://localhost:8080",
		sessKey:       bytes.Repeat([]byte("k"), 32),
	}, options{}, testLogger())
	require.NoError(t, err)
	defer cancelBg()

	srv := httptest.NewServer(mux)
	defer srv.Close()

	code, loc := getNoRedirect(t, srv.URL+"/")
	assert.Equal(t, http.StatusSeeOther, code)
	assert.Contains(t, loc, "/login")
}

// headersOf serves h and returns the response headers for a GET of path.
func headersOf(t *testing.T, h http.Handler, path string) http.Header {
	t.Helper()
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Get(srv.URL + path) //nolint:noctx // test-only helper, no request context needed.
	require.NoError(t, err)
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.Header
}

// TestAssembleMuxSecurityHeaders asserts the hardening headers apply to every
// route: nosniff always, and HSTS only for an https deployment (matching the
// Secure-cookie gate).
func TestAssembleMuxSecurityHeaders(t *testing.T) {
	// Insecure (http base URL): nosniff always, no HSTS.
	insecure, _, cancel1, err := assembleMux(builtDeps{
		querier: nopQuerier{}, ingestHandler: newIngestHandler(t),
		constTenant: "dev-tenant", baseURL: "http://localhost:8080",
	}, options{}, testLogger())
	require.NoError(t, err)
	defer cancel1()
	h1 := headersOf(t, insecure, "/healthz")
	assert.Equal(t, "nosniff", h1.Get("X-Content-Type-Options"))
	assert.Empty(t, h1.Get("Strict-Transport-Security"))

	// Secure (https base URL): HSTS present, on the same route.
	secure, _, cancel2, err := assembleMux(builtDeps{
		querier: nopQuerier{}, ingestHandler: newIngestHandler(t),
		constTenant: "dev-tenant", baseURL: "https://maping.example.com",
	}, options{}, testLogger())
	require.NoError(t, err)
	defer cancel2()
	h2 := headersOf(t, secure, "/healthz")
	assert.Equal(t, "nosniff", h2.Get("X-Content-Type-Options"))
	assert.Contains(t, h2.Get("Strict-Transport-Security"), "max-age=")
}

// TestResolveControlPlaneDevMode asserts that with no store the derived
// collaborators are all zero/nil (the static dev path), no error.
func TestResolveControlPlaneDevMode(t *testing.T) {
	d, err := resolveControlPlane(nil, options{}, "http://localhost", testLogger())
	require.NoError(t, err)
	assert.Nil(t, d.cp)
	assert.Nil(t, d.memberStore)
	assert.Nil(t, d.pool)
	assert.Nil(t, d.sessKey)
}

// TestWithMaxBody verifies the body-size guardrail rejects over-limit bodies.
// The rest of Serve() blocks on signals and is exercised end-to-end by the
// integration test, not here.
func TestWithMaxBody(t *testing.T) {
	var got int
	h := withMaxBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := io.Copy(io.Discard, r.Body)
		got = int(n)
		if err != nil {
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	}), 8)

	// Under the limit.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("hello")))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 5, got)

	// Over the limit -> MaxBytesReader errors on read.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("way too many bytes")))
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}
