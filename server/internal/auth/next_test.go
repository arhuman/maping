package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/oauth2"
)

func TestSafeNext(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "/"},
		{"/", "/"},
		{"/dashboard", "/dashboard"},
		{"/reports/view?range=7d", "/reports/view?range=7d"},
		{"//evil.com", "/"},           // scheme-relative -> external
		{"https://evil.com", "/"},     // absolute URL
		{"http://evil.com/path", "/"}, // absolute URL
		{`/\evil.com`, "/"},           // backslash trick browsers read as //
		{"javascript:alert(1)", "/"},  // no leading slash
		{"relative/path", "/"},        // not rooted
		{" /leading-space", "/"},      // does not start with "/"
	}
	for _, c := range cases {
		if got := safeNext(c.in); got != c.want {
			t.Errorf("safeNext(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestHandleStartStoresSanitizedNext drives the login start and reads the state
// cookie back: a safe ?next is preserved for the callback, an unsafe one is
// clamped to "/" at write time so a poisoned link never survives the round trip.
func TestHandleStartStoresSanitizedNext(t *testing.T) {
	for _, c := range []struct{ query, wantNext string }{
		{"next=%2Freports%2Fview%3Frange%3D7d", "/reports/view?range=7d"},
		{"next=https%3A%2F%2Fevil.com", "/"},
		{"", "/"},
	} {
		a := providerAuth(t, &fakeStore{}, "http://unused")
		req := httptest.NewRequest(http.MethodGet, "/auth/github/start?"+c.query, nil)
		req.SetPathValue("provider", "github")
		rec := httptest.NewRecorder()
		a.handleStart(rec, req)

		// Read the planted state cookie back through the real reader.
		back := httptest.NewRequest(http.MethodGet, "/", nil)
		back.AddCookie(rec.Result().Cookies()[0])
		_, _, next, ok := a.readStateCookie(back)
		if !ok {
			t.Fatalf("query %q: state cookie did not verify", c.query)
		}
		if next != c.wantNext {
			t.Errorf("query %q: stored next = %q, want %q", c.query, next, c.wantNext)
		}
	}
}

// callbackWithNext drives handleCallback with a state cookie that carries next,
// for a returning (isNew=false) login, and returns the recorder.
func callbackWithNext(t *testing.T, next string) *httptest.ResponseRecorder {
	t.Helper()
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"tok","token_type":"bearer"}`))
	}))
	t.Cleanup(tokenSrv.Close)

	a := providerAuth(t, &fakeStore{}, tokenSrv.URL)
	a.userinfo = func(_ context.Context, _ providerName, _ *oauth2.Config, _ *oauth2.Token) (identity, error) {
		return identity{Subject: "github:42", Email: "u@example.com"}, nil
	}
	const state = "the-state"
	req := httptest.NewRequest(http.MethodGet, "/auth/github/callback?code=abc&state="+state, nil)
	req.SetPathValue("provider", "github")
	req.AddCookie(signedStateCookie(a, "github|"+state+"|"+next))

	rec := httptest.NewRecorder()
	a.handleCallback(rec, req)
	return rec
}

func TestCallbackReturningUserHonorsNext(t *testing.T) {
	rec := callbackWithNext(t, "/reports/view?range=7d")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/reports/view?range=7d" {
		t.Errorf("post-login redirect = %q, want the next target", loc)
	}
}

func TestCallbackClampsUnsafeNext(t *testing.T) {
	// A signed cookie can't be forged, but the redirect re-sanitizes as defense in
	// depth: even an external next never becomes an open redirect.
	rec := callbackWithNext(t, "https://evil.com")
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Errorf("unsafe next redirect = %q, want / (clamped)", loc)
	}
}

func TestDevLoginHonorsNext(t *testing.T) {
	a := newTestAuth(t, &fakeStore{})
	form := strings.NewReader("next=%2Freports%2Fview%3Frange%3D30d")
	req := httptest.NewRequest(http.MethodPost, "/auth/dev/login", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	a.handleDevLogin(rec, req)
	if loc := rec.Header().Get("Location"); loc != "/reports/view?range=30d" {
		t.Errorf("dev-login redirect = %q, want the next target", loc)
	}
}

func TestLoginPageCarriesNextIntoStartLinks(t *testing.T) {
	a := providerAuth(t, &fakeStore{}, "http://unused")
	rec := httptest.NewRecorder()
	a.handleLoginPage(rec, httptest.NewRequest(http.MethodGet, "/login?next=%2Freports%2Fview%3Frange%3D7d", nil))
	// html/template's URL normalizer lowercases percent-escapes (%2f, not %2F);
	// per RFC 3986 that is equivalent and both browsers and net/url decode it the
	// same, so assert case-insensitively.
	body := strings.ToLower(rec.Body.String())
	if !strings.Contains(body, "/auth/github/start?next=%2freports%2fview%3frange%3d7d") {
		t.Errorf("login page must carry the sanitized next into the GitHub start link; body:\n%s", rec.Body.String())
	}

	// With no next, the start link stays clean (no dangling query).
	rec2 := httptest.NewRecorder()
	a.handleLoginPage(rec2, httptest.NewRequest(http.MethodGet, "/login", nil))
	if strings.Contains(rec2.Body.String(), "/auth/github/start?next=") {
		t.Error("login page without next must not append a next query to the start link")
	}
}
