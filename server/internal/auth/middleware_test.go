package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// okHandler records whether it was reached and echoes the context tenant.
func okHandler(reached *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*reached = true
		if org, ok := TenantFromContext(r); ok {
			_, _ = w.Write([]byte(org))
		}
		w.WriteHeader(http.StatusOK)
	})
}

func TestMiddlewareAllowsValidSession(t *testing.T) {
	a := newTestAuth(t, &fakeStore{})
	var reached bool
	h := a.Middleware(okHandler(&reached))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{
		Name:  sessionCookie,
		Value: a.signer.encode(newSession("org-9", "mem-9", "member")),
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !reached {
		t.Fatal("valid session must reach next handler")
	}
	if rec.Body.String() != "org-9" {
		t.Errorf("TenantFromContext body = %q, want org-9", rec.Body.String())
	}
}

func TestMiddlewareRedirectsBrowserWhenLoggedOut(t *testing.T) {
	a := newTestAuth(t, &fakeStore{})
	var reached bool
	h := a.Middleware(okHandler(&reached))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/services/foo", nil))

	if reached {
		t.Fatal("logged-out request must not reach next")
	}
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Errorf("got %d -> %q, want 303 -> /login", rec.Code, rec.Header().Get("Location"))
	}
}

func TestMiddlewareReturns401JSONForAPI(t *testing.T) {
	a := newTestAuth(t, &fakeStore{})
	var reached bool
	h := a.Middleware(okHandler(&reached))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/series", nil))

	if reached {
		t.Fatal("logged-out API request must not reach next")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("API status = %d, want 401", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q, want application/json", ct)
	}
}

func TestMiddlewareRejectsExpiredCookie(t *testing.T) {
	a := newTestAuth(t, &fakeStore{})
	var reached bool
	h := a.Middleware(okHandler(&reached))

	expired := Session{OrgID: "org-1", Exp: time.Now().Add(-time.Hour)}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: a.signer.encode(expired)})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if reached {
		t.Fatal("expired cookie must be treated as logged out")
	}
	if rec.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303 redirect", rec.Code)
	}
}

func TestTenantFromContextUnauthenticated(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if _, ok := TenantFromContext(req); ok {
		t.Fatal("bare request must be unauthenticated")
	}
}
