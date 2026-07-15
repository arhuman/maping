package app

import (
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

// TestWithMaxBody verifies the body-size guardrail rejects over-limit bodies.
// The rest of Run() blocks on signals and is exercised end-to-end by the
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
