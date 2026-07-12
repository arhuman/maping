package app

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arhuman/maping/server/internal/web"
)

func TestHealthHandler(t *testing.T) {
	var ready atomic.Bool
	ready.Store(true)
	h := healthHandler(&ready)

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ok", rec.Body.String())

	// Once shutdown flips readiness, the probe fails so LBs stop routing.
	ready.Store(false)
	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestNewHTTPServer(t *testing.T) {
	srv := newHTTPServer("", http.NewServeMux())
	assert.Equal(t, ":8080", srv.Addr, "empty listen defaults to :8080")

	srv = newHTTPServer(":9999", http.NewServeMux())
	assert.Equal(t, ":9999", srv.Addr)
	assert.NotZero(t, srv.ReadHeaderTimeout, "a read-header timeout is set")
}

func TestMountDashboardOpenWhenNoAuth(t *testing.T) {
	// auth off: the dashboard is reachable at "/" without a session.
	webHandler, err := web.NewHandler(buildWebConfig(nopQuerier{}, nil, nil, false, devTenant, "", nil, testLogger()))
	require.NoError(t, err)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthHandler(readyBool()))
	mountDashboard(mux, webHandler, nil, testLogger())

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, http.StatusOK, rec.Code, "open dashboard renders at /")

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestMountDashboardGatedWhenAuth(t *testing.T) {
	// auth on: "/" is behind the session gate, so an unauthenticated request is
	// redirected to /login rather than served the dashboard.
	authLayer, err := buildAuth(fakeMemberStore{}, "https://maping.example.com", csrfKey, testLogger())
	require.NoError(t, err)
	require.NotNil(t, authLayer)

	webHandler, err := web.NewHandler(buildWebConfig(nopQuerier{}, &fakeControlPlane{}, nil, true, devTenant, "https://maping.example.com", csrfKey, testLogger()))
	require.NoError(t, err)

	mux := http.NewServeMux()
	mountDashboard(mux, webHandler, authLayer, testLogger())

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, http.StatusSeeOther, rec.Code, "gated dashboard redirects unauthenticated callers")
	assert.Equal(t, "/login", rec.Header().Get("Location"))

	// The open login route registered by the auth layer is reachable.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
}

// readyBool returns a ready-true atomic bool for handler wiring tests.
func readyBool() *atomic.Bool {
	var b atomic.Bool
	b.Store(true)
	return &b
}
