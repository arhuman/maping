package auth

import (
	"context"
	"net/http"
	"strings"
)

// ctxKey is the private context key type for the authenticated session, so no
// other package can collide with or forge it.
type ctxKey struct{}

// withSession returns a child context carrying the verified session.
func withSession(ctx context.Context, sess Session) context.Context {
	return context.WithValue(ctx, ctxKey{}, sess)
}

// FromContext returns the authenticated session stored by the Middleware, or
// ok=false when the request is unauthenticated.
func FromContext(ctx context.Context) (Session, bool) {
	sess, ok := ctx.Value(ctxKey{}).(Session)
	return sess, ok
}

// TenantFromContext returns the authenticated org id for a request, satisfying
// web's TenantResolver signature shape via the request context. main wires this
// into web.Config.Tenant, so the dashboard renders only the caller's org.
func TenantFromContext(r *http.Request) (string, bool) {
	sess, ok := FromContext(r.Context())
	if !ok {
		return "", false
	}
	return sess.OrgID, true
}

// Middleware verifies the session cookie and, on success, stores the identity in
// the request context before calling next. On failure it redirects a browser
// request to /login, or returns 401 JSON for an /api/ path (so htmx/fetch
// callers get a machine-readable status instead of an HTML redirect body).
func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, ok := a.sessionFromRequest(r)
		if !ok {
			a.denyUnauthenticated(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(withSession(r.Context(), sess)))
	})
}

// Authenticated reports whether the request carries a valid session cookie,
// without requiring the Middleware to have run. The composition root uses it at
// "/" to serve the public home to anonymous visitors while routing signed-in
// users to the dashboard.
func (a *Auth) Authenticated(r *http.Request) bool {
	_, ok := a.sessionFromRequest(r)
	return ok
}

// sessionFromRequest reads and verifies the session cookie. A missing, expired,
// or tampered cookie yields ok=false (treated as logged out).
func (a *Auth) sessionFromRequest(r *http.Request) (Session, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return Session{}, false
	}
	sess, err := a.signer.decode(c.Value, timeNow())
	if err != nil {
		return Session{}, false
	}
	return sess, true
}

// denyUnauthenticated writes the unauthenticated response: 401 JSON for API
// paths, a redirect to /login otherwise.
func (a *Auth) denyUnauthenticated(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
