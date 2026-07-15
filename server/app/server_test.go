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

// stubHome is a stand-in public-home handler for the dashboard-wiring tests (the
// real marketing home lives in a composing build now).
func stubHome() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("PUBLIC HOME"))
	}
}

func TestMountDashboardOpenWhenNoAuth(t *testing.T) {
	// auth off: the dashboard is reachable at "/" without a session, and there is
	// no public home (self-host/dev mode).
	webHandler, err := web.NewHandler(buildWebConfig(nopQuerier{}, nil, nil, nil, false, devTenant, "", nil, testLogger()))
	require.NoError(t, err)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthHandler(readyBool()))
	mountDashboard(mux, webHandler, nil, nil, testLogger())

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, http.StatusOK, rec.Code, "open dashboard renders at /")

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestMountDashboardAuthAnonSeesPublicHome(t *testing.T) {
	// auth on with a public home wired: an anonymous "/" serves the home (not a
	// redirect), while dashboard sub-paths stay gated. Companion routes like
	// /pricing are the composing build's concern (WithRoutes), not mountDashboard's.
	authLayer, err := buildAuth(fakeMemberStore{}, nil, "https://maping.example.com", csrfKey, testLogger())
	require.NoError(t, err)
	require.NotNil(t, authLayer)

	webHandler, err := web.NewHandler(buildWebConfig(nopQuerier{}, &fakeControlPlane{}, nil, nil, true, devTenant, "https://maping.example.com", csrfKey, testLogger()))
	require.NoError(t, err)

	mux := http.NewServeMux()
	mountDashboard(mux, webHandler, stubHome(), authLayer, testLogger())

	// Anonymous "/" -> public home (200), not a redirect.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, http.StatusOK, rec.Code, "anonymous / serves the public home")
	assert.Contains(t, rec.Body.String(), "PUBLIC HOME")

	// A dashboard sub-path is still gated: anonymous -> redirect to /login.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/setup", nil))
	assert.Equal(t, http.StatusSeeOther, rec.Code, "sub-paths stay behind the auth gate")
	assert.Equal(t, "/login", rec.Header().Get("Location"))

	// The open login route is reachable.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestMountDashboardAuthNoPublicHomeGatesRoot(t *testing.T) {
	// No public home (nil handler): "/" is the gated dashboard, so an anonymous
	// visitor is redirected to /login instead of seeing a public home.
	authLayer, err := buildAuth(fakeMemberStore{}, nil, "https://maping.example.com", csrfKey, testLogger())
	require.NoError(t, err)
	require.NotNil(t, authLayer)

	webHandler, err := web.NewHandler(buildWebConfig(nopQuerier{}, &fakeControlPlane{}, nil, nil, true, devTenant, "https://maping.example.com", csrfKey, testLogger()))
	require.NoError(t, err)

	mux := http.NewServeMux()
	mountDashboard(mux, webHandler, nil, authLayer, testLogger())

	// Anonymous "/" -> gated -> redirect to /login (no public home).
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, http.StatusSeeOther, rec.Code, "anon / is gated when no public home is wired")
	assert.Equal(t, "/login", rec.Header().Get("Location"))
}

func TestRootHandlerDispatch(t *testing.T) {
	// rootHandler serves marketing to anonymous "/", and the gated dashboard to a
	// signed-in "/" and to every non-root path.
	authLayer, err := buildAuth(fakeMemberStore{}, nil, "https://maping.example.com", csrfKey, testLogger())
	require.NoError(t, err)

	// Mint a real session cookie via the dev-login route.
	loginMux := http.NewServeMux()
	authLayer.Register(loginMux)
	loginRec := httptest.NewRecorder()
	loginMux.ServeHTTP(loginRec, httptest.NewRequest(http.MethodPost, "/auth/dev/login", nil))
	cookies := loginRec.Result().Cookies()
	require.NotEmpty(t, cookies, "dev-login must mint a session cookie")

	gated := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("DASHBOARD")) })
	home := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("MARKETING")) })
	h := rootHandler(authLayer, gated, home)

	// Anonymous "/" -> marketing.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, "MARKETING", rec.Body.String())

	// Signed-in "/" -> gated dashboard.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, "DASHBOARD", rec.Body.String())

	// Anonymous non-root path -> gated (never marketing).
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/setup", nil))
	assert.Equal(t, "DASHBOARD", rec.Body.String())
}

// readyBool returns a ready-true atomic bool for handler wiring tests.
func readyBool() *atomic.Bool {
	var b atomic.Bool
	b.Store(true)
	return &b
}
