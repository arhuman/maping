package app

import (
	"context"
	"log/slog"
	"net/http"
	"sync/atomic"

	"github.com/arhuman/maping/server/internal/auth"
	"github.com/arhuman/maping/server/internal/control"
	"github.com/arhuman/maping/server/internal/guardrail"
	"github.com/arhuman/maping/server/internal/web"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arhuman/maping/proto/maping/v1/mapingv1connect"
)

// cpDeps are the control-plane-derived collaborators build hands to assembleMux:
// the session-signing key, the auth member store and dashboard control surface,
// the composed login interceptor and team admin, and the pgx pool. All are
// zero/nil in static dev mode (store == nil).
type cpDeps struct {
	sessKey     []byte
	memberStore auth.MemberStore
	cp          controlPlane
	interceptor LoginInterceptor
	memberAdmin MemberAdmin
	pool        *pgxpool.Pool
}

// resolveControlPlane derives the auth/session/team collaborators from the
// control-plane store, or returns the zero value when there is none (static dev
// mode). The session key is resolved here because it and the store together gate
// every downstream auth decision. Assigning the concrete non-nil store into the
// interface fields avoids a typed-nil that would defeat the nil checks in
// assembleMux/buildWebConfig.
func resolveControlPlane(store *control.Store, o options, baseURL string, log *slog.Logger) (cpDeps, error) {
	if store == nil {
		return cpDeps{}, nil
	}
	sessKey, err := sessionKey(secureFromBaseURL(baseURL), log)
	if err != nil {
		return cpDeps{}, err
	}
	d := cpDeps{sessKey: sessKey, memberStore: store, cp: store, pool: store.Pool()}
	// The composed post-auth hook (invitation accept) and team-panel admin need the
	// pool; both stay nil without one, so dev mode is plain login with no team panel.
	if o.loginInterceptor != nil {
		d.interceptor = o.loginInterceptor(d.pool)
	}
	if o.memberAdmin != nil {
		d.memberAdmin = o.memberAdmin(d.pool)
	}
	return d, nil
}

// builtDeps groups the already-constructed collaborators assembleMux mounts, so
// build can supply the live writer-backed ones and a unit test can supply fakes.
// Every control-plane field is nil in static dev mode.
type builtDeps struct {
	querier       web.Querier
	ingestHandler mapingv1connect.IngestServiceHandler
	cp            controlPlane
	memberStore   auth.MemberStore
	interceptor   LoginInterceptor
	memberAdmin   MemberAdmin
	pool          *pgxpool.Pool
	card          *guardrail.Cardinality
	constTenant   string
	baseURL       string
	sessKey       []byte
}

// assembleMux builds the fully-routed HTTP handler and the readiness flag from
// already-constructed collaborators. It concentrates every control-plane-
// dependent routing decision (auth gating, key-admin/team panels, public home,
// extension routes) and touches no infra, so build supplies live writer-backed
// deps while a unit test supplies fakes. The returned CancelFunc stops the
// background jobs and must be called on shutdown.
func assembleMux(d builtDeps, o options, log *slog.Logger) (http.Handler, *atomic.Bool, context.CancelFunc, error) {
	authLayer, err := buildAuth(d.memberStore, d.interceptor, d.baseURL, d.sessKey, log)
	if err != nil {
		return nil, nil, nil, err
	}
	webCfg := buildWebConfig(d.querier, d.cp, d.memberAdmin, d.card, authLayer != nil, d.constTenant, d.baseURL, d.sessKey, log)
	webCfg.AccountHref = o.accountHref
	webHandler, err := web.NewHandler(webCfg)
	if err != nil {
		return nil, nil, nil, err
	}

	// readiness flips to false on shutdown so load balancers stop routing.
	ready := new(atomic.Bool)
	ready.Store(true)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthHandler(ready))
	// The public landing page ("/" for anonymous visitors) is opt-in via
	// WithPublicHome; a self-host/OSS deployment wires none, so anonymous "/"
	// redirects to /login. A composing build supplies the landing page and
	// registers any companion routes via WithRoutes.
	mountDashboard(mux, webHandler, o.publicHome, authLayer, log)
	mountIngest(mux, d.ingestHandler, maxBodyCeiling(log))

	// Extension routes mount after the core surfaces so their patterns compose by
	// ServeMux precedence. The pool is nil in static dev mode (no control plane).
	// gate/sessionOrg give a registrar the dashboard auth middleware and the
	// verified-org reader (both nil when auth is off), so a composing build mounts
	// its own authenticated routes without importing the internal auth package.
	var (
		gate       func(http.Handler) http.Handler
		sessionOrg func(*http.Request) (string, bool)
	)
	if authLayer != nil {
		gate = authLayer.Middleware
		sessionOrg = auth.TenantFromContext
	}
	mountExtensions(mux, o.routes, d.pool, gate, sessionOrg, webHandler.RenderShellPage, log)

	// Background loops (extension jobs) share one context, cancelled on shutdown so
	// every goroutine exits before the pool closes.
	bgCtx, cancelBg := context.WithCancel(context.Background())
	startBackgroundJobs(bgCtx, o.jobs, d.pool, log)

	// Wrap the whole mux so the hardening headers apply to every route (dashboard,
	// auth, ingest, healthz), not just the HTML render path that sets the CSP.
	return securityHeaders(mux, secureFromBaseURL(d.baseURL)), ready, cancelBg, nil
}

// securityHeaders wraps h to set response-hardening headers on every route:
// X-Content-Type-Options: nosniff always (defeat MIME sniffing), and
// Strict-Transport-Security only for an https deployment (secure), matching the
// Secure session-cookie gate. HSTS over plain-http dev would be ignored by
// browsers anyway; gating it keeps dev/prod behavior explicit and avoids pinning
// localhost to https.
func securityHeaders(h http.Handler, secure bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if secure {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		h.ServeHTTP(w, r)
	})
}
