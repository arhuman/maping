package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// errTest is a scripted failure for the fake store.
var errTest = errors.New("test error")

// fakeStore is a MemberStore that records calls and returns scripted identities.
type fakeStore struct {
	upsertSubject string
	upsertEmail   string
	upsertIsNew   bool // when true, UpsertMemberFromOIDC reports a first login
	upsertErr     error
	devErr        error
	issuedOrg     string // records the org IssueKey was called for
	issueErr      error
}

func (f *fakeStore) UpsertMemberFromOIDC(_ context.Context, subject, email string) (string, string, string, bool, error) {
	f.upsertSubject = subject
	f.upsertEmail = email
	if f.upsertErr != nil {
		return "", "", "", false, f.upsertErr
	}
	return "org-from-oidc", "member-1", "admin", f.upsertIsNew, nil
}

func (f *fakeStore) DevOrgAdmin(_ context.Context, _ string) (string, string, error) {
	if f.devErr != nil {
		return "", "", f.devErr
	}
	return "dev-org-id", "dev-member-id", nil
}

func (f *fakeStore) IssueKey(_ context.Context, orgID, _ string) (string, error) {
	f.issuedOrg = orgID
	if f.issueErr != nil {
		return "", f.issueErr
	}
	return "topsecret", nil
}

// newTestAuth builds an Auth with a fake store, a fake userinfo, and no real
// providers by default (so dev-login is on). Callers override fields as needed.
func newTestAuth(t *testing.T, store MemberStore) *Auth {
	t.Helper()
	a, err := New(Config{
		Store:      store,
		SessionKey: testKey,
		DevOrgName: "dev-org",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

func TestDevLoginEnabledWithoutProviders(t *testing.T) {
	a := newTestAuth(t, &fakeStore{})
	if !a.devLogin {
		t.Fatal("dev-login must be enabled when no provider configured")
	}
	providers, dev := a.Enabled()
	if len(providers) != 0 || !dev {
		t.Fatalf("Enabled() = (%v, %v), want ([], true)", providers, dev)
	}
}

func TestDevLoginDisabledWhenProviderConfigured(t *testing.T) {
	a, err := New(Config{
		Store:      &fakeStore{},
		SessionKey: testKey,
		Providers: ProviderConfig{
			GitHubClientID:     "id",
			GitHubClientSecret: "secret",
			BaseURL:            "https://maping.example.com",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.devLogin {
		t.Fatal("dev-login must be OFF when a real provider is configured")
	}
}

func TestHandleDevLoginSetsSession(t *testing.T) {
	a := newTestAuth(t, &fakeStore{})
	rec := httptest.NewRecorder()
	a.handleDevLogin(rec, httptest.NewRequest(http.MethodPost, "/auth/dev/login", nil))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	sess := sessionFromSetCookie(t, a, rec)
	if sess.OrgID != "dev-org-id" || sess.Role != "admin" {
		t.Errorf("dev session = %+v, want dev-org-id/admin", sess)
	}
}

func TestHandleDevLoginStoreError(t *testing.T) {
	// A control-plane failure resolving the dev admin is a 500, not a session.
	a := newTestAuth(t, &fakeStore{devErr: errTest})
	rec := httptest.NewRecorder()
	a.handleDevLogin(rec, httptest.NewRequest(http.MethodPost, "/auth/dev/login", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("dev-login store error status = %d, want 500", rec.Code)
	}
}

func TestCallbackStateMismatchRejected(t *testing.T) {
	a := providerAuth(t, &fakeStore{}, "http://unused")
	req := httptest.NewRequest(http.MethodGet, "/auth/github/callback?code=c&state=attacker", nil)
	req.SetPathValue("provider", "github")
	// State cookie carries a different, legitimate value.
	req.AddCookie(signedStateCookie(a, "github|legit"))

	rec := httptest.NewRecorder()
	a.handleCallback(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("state mismatch status = %d, want 400", rec.Code)
	}
}

func TestCallbackSuccessSetsSessionAndUpserts(t *testing.T) {
	// Stub token endpoint so Exchange succeeds without the real network.
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"tok","token_type":"bearer"}`))
	}))
	defer tokenSrv.Close()

	store := &fakeStore{}
	a := providerAuth(t, store, tokenSrv.URL)
	// Inject a fake userinfo so no real GitHub call happens.
	a.userinfo = func(_ context.Context, _ providerName, _ *oauth2.Config, _ *oauth2.Token) (identity, error) {
		return identity{Subject: "github:42", Email: "u@example.com"}, nil
	}

	const state = "the-state"
	req := httptest.NewRequest(http.MethodGet, "/auth/github/callback?code=abc&state="+state, nil)
	req.SetPathValue("provider", "github")
	req.AddCookie(signedStateCookie(a, "github|"+state))

	rec := httptest.NewRecorder()
	a.handleCallback(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("callback status = %d body=%q, want 303", rec.Code, rec.Body.String())
	}
	if store.upsertSubject != "github:42" || store.upsertEmail != "u@example.com" {
		t.Errorf("upsert got (%q,%q)", store.upsertSubject, store.upsertEmail)
	}
	sess := sessionFromSetCookie(t, a, rec)
	if sess.OrgID != "org-from-oidc" {
		t.Errorf("session org = %q, want org-from-oidc", sess.OrgID)
	}
}

func TestCallbackUnknownProvider(t *testing.T) {
	a := providerAuth(t, &fakeStore{}, "http://unused")
	req := httptest.NewRequest(http.MethodGet, "/auth/google/callback?code=c&state=s", nil)
	req.SetPathValue("provider", "google") // google is not configured
	rec := httptest.NewRecorder()
	a.handleCallback(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown provider status = %d, want 404", rec.Code)
	}
}

func TestCallbackMissingCode(t *testing.T) {
	a := providerAuth(t, &fakeStore{}, "http://unused")
	const state = "the-state"
	req := httptest.NewRequest(http.MethodGet, "/auth/github/callback?state="+state, nil) // no code
	req.SetPathValue("provider", "github")
	req.AddCookie(signedStateCookie(a, "github|"+state))
	rec := httptest.NewRecorder()
	a.handleCallback(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing code status = %d, want 400", rec.Code)
	}
}

func TestCallbackExchangeFailure(t *testing.T) {
	// The token endpoint rejects the exchange, so Exchange returns an error.
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "invalid_grant", http.StatusBadRequest)
	}))
	defer tokenSrv.Close()

	a := providerAuth(t, &fakeStore{}, tokenSrv.URL)
	const state = "the-state"
	req := httptest.NewRequest(http.MethodGet, "/auth/github/callback?code=abc&state="+state, nil)
	req.SetPathValue("provider", "github")
	req.AddCookie(signedStateCookie(a, "github|"+state))
	rec := httptest.NewRecorder()
	a.handleCallback(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("exchange failure status = %d, want 502", rec.Code)
	}
}

func TestCallbackUserinfoFailure(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"tok","token_type":"bearer"}`))
	}))
	defer tokenSrv.Close()

	a := providerAuth(t, &fakeStore{}, tokenSrv.URL)
	a.userinfo = func(context.Context, providerName, *oauth2.Config, *oauth2.Token) (identity, error) {
		return identity{}, errTest // provider userinfo call fails
	}
	const state = "the-state"
	req := httptest.NewRequest(http.MethodGet, "/auth/github/callback?code=abc&state="+state, nil)
	req.SetPathValue("provider", "github")
	req.AddCookie(signedStateCookie(a, "github|"+state))
	rec := httptest.NewRecorder()
	a.handleCallback(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("userinfo failure status = %d, want 502", rec.Code)
	}
}

func TestCallbackUpsertFailure(t *testing.T) {
	// A control-plane write failure surfaces as 500, not a partial session.
	store := &fakeStore{upsertErr: errTest}
	rec := callbackReq(t, store)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("upsert failure status = %d, want 500", rec.Code)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && c.MaxAge >= 0 && c.Value != "" {
			t.Error("no session cookie must be set when the upsert fails")
		}
	}
}

func TestHandleStartRedirectsAndSetsState(t *testing.T) {
	a := providerAuth(t, &fakeStore{}, "http://unused")
	req := httptest.NewRequest(http.MethodGet, "/auth/github/start", nil)
	req.SetPathValue("provider", "github")
	rec := httptest.NewRecorder()
	a.handleStart(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("start status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "https://github.com/login/oauth/authorize") {
		t.Errorf("start redirect = %q, want the provider authorize URL", loc)
	}
	// A signed state cookie must be planted for the callback to verify against.
	var found bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == stateCookie && c.Value != "" {
			found = true
		}
	}
	if !found {
		t.Error("handleStart must set the oauth state cookie")
	}
}

func TestHandleStartUnknownProvider(t *testing.T) {
	a := providerAuth(t, &fakeStore{}, "http://unused")
	req := httptest.NewRequest(http.MethodGet, "/auth/google/start", nil)
	req.SetPathValue("provider", "google")
	rec := httptest.NewRecorder()
	a.handleStart(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown provider start status = %d, want 404", rec.Code)
	}
}

func TestHandleLogoutClearsSession(t *testing.T) {
	a := newTestAuth(t, &fakeStore{})
	rec := httptest.NewRecorder()
	a.handleLogout(rec, httptest.NewRequest(http.MethodPost, "/logout", nil))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("logout status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login" {
		t.Errorf("logout redirect = %q, want /login", loc)
	}
	// The session cookie is expired (MaxAge < 0).
	var cleared bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("logout must expire the session cookie")
	}
}

// callbackReq drives handleCallback through a stubbed token endpoint + fake
// userinfo for a first/returning login, returning the recorder.
func callbackReq(t *testing.T, store *fakeStore) *httptest.ResponseRecorder {
	t.Helper()
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"tok","token_type":"bearer"}`))
	}))
	t.Cleanup(tokenSrv.Close)

	a := providerAuth(t, store, tokenSrv.URL)
	a.userinfo = func(_ context.Context, _ providerName, _ *oauth2.Config, _ *oauth2.Token) (identity, error) {
		return identity{Subject: "github:42", Email: "u@example.com"}, nil
	}
	const state = "the-state"
	req := httptest.NewRequest(http.MethodGet, "/auth/github/callback?code=abc&state="+state, nil)
	req.SetPathValue("provider", "github")
	req.AddCookie(signedStateCookie(a, "github|"+state))

	rec := httptest.NewRecorder()
	a.handleCallback(rec, req)
	return rec
}

func TestCallbackFirstLoginRevealsKey(t *testing.T) {
	store := &fakeStore{upsertIsNew: true}
	rec := callbackReq(t, store)

	if rec.Code != http.StatusOK {
		t.Fatalf("first-login status = %d, want 200 interstitial", rec.Code)
	}
	if store.issuedOrg != "org-from-oidc" {
		t.Errorf("IssueKey called for org %q, want org-from-oidc", store.issuedOrg)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "export MAPING_KEY=mk_live_") || !strings.Contains(body, "topsecret") {
		t.Errorf("interstitial must reveal the full token once; body=%q", body)
	}
	if !strings.Contains(body, "COPY IT NOW") {
		t.Error("interstitial must warn the key is shown once")
	}
	// The copy button (data-copy hook) must be activated by the shared helper.
	if !strings.Contains(body, `data-copy="mp-key"`) {
		t.Error("interstitial must carry the data-copy hook")
	}
	if !strings.Contains(body, `src="/assets/copy.js"`) {
		t.Error("interstitial must load the copy helper to activate the button")
	}
	// The JS budget is enforced by CSP: only self-hosted scripts run.
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") {
		t.Errorf("interstitial CSP must pin script-src 'self'; got %q", csp)
	}
}

func TestCallbackFirstLoginKeyIssueErrorFallsThrough(t *testing.T) {
	// A key-issue failure must not strand the user: fall through to the dashboard.
	store := &fakeStore{upsertIsNew: true, issueErr: errTest}
	rec := callbackReq(t, store)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("issue-error status = %d, want 303 fallback", rec.Code)
	}
}

// providerAuth builds an Auth with a single GitHub provider whose token URL is
// overridden to tokenURL (so Exchange hits a stub, not github.com).
func providerAuth(t *testing.T, store MemberStore, tokenURL string) *Auth {
	t.Helper()
	a, err := New(Config{
		Store:      store,
		SessionKey: testKey,
		Providers: ProviderConfig{
			GitHubClientID:     "id",
			GitHubClientSecret: "secret",
			BaseURL:            "https://maping.example.com",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := a.providers[providerGitHub]
	p.config.Endpoint = oauth2.Endpoint{
		AuthURL:  "https://github.com/login/oauth/authorize",
		TokenURL: tokenURL,
	}
	a.providers[providerGitHub] = p
	return a
}

// signedStateCookie builds a valid signed state cookie for payload.
func signedStateCookie(a *Auth, payload string) *http.Cookie {
	rec := httptest.NewRecorder()
	a.setStateCookie(rec, payload)
	return rec.Result().Cookies()[0]
}

// sessionFromSetCookie extracts and decodes the session cookie from a response.
func sessionFromSetCookie(t *testing.T, a *Auth, rec *httptest.ResponseRecorder) Session {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			sess, err := a.signer.decode(c.Value, time.Now())
			if err != nil {
				t.Fatalf("decode session cookie: %v", err)
			}
			return sess
		}
	}
	t.Fatal("no session cookie set")
	return Session{}
}

func TestLoginPageListsProvidersAndDevLogin(t *testing.T) {
	a := newTestAuth(t, &fakeStore{})
	rec := httptest.NewRecorder()
	a.handleLoginPage(rec, httptest.NewRequest(http.MethodGet, "/login", nil))
	if !strings.Contains(rec.Body.String(), "Dev login") {
		t.Error("dev-login button missing from login page")
	}
}

var _ = url.Values{}
