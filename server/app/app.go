// Package app is the maping-server composition root. It wires the storage
// writer, ingest guardrails, control plane, auth layer, and dashboard from the
// environment and runs the HTTP server until a shutdown signal.
//
// The wiring lives here rather than in package main so the control-plane-
// dependent decisions — whether the dashboard is auth-gated, the self-serve
// key-admin adapter, and the CSRF/session-key plumbing — are unit-testable
// against fakes without a live Postgres. main is a thin shell over Run.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/arhuman/maping/server/internal/auth"
	"github.com/arhuman/maping/server/internal/control"
	"github.com/arhuman/maping/server/internal/guardrail"
	"github.com/arhuman/maping/server/internal/ingest"
	"github.com/arhuman/maping/server/internal/storage"
	"github.com/arhuman/maping/server/internal/web"

	"github.com/arhuman/maping/proto/maping/v1/mapingv1connect"
	"github.com/arhuman/maping/proto/mapingcompress"
)

const (
	// defaultMaxBodyCeiling is the absolute HTTP-layer body cap applied pre-auth,
	// before the tenant is known. It is a HARD memory-safety bound
	// that must be >= the largest plan's max_payload_bytes; the per-tenant plan
	// limit is the logical/fairness bound, enforced after auth inside Upload. It
	// is overridable via MAPING_MAX_BODY_BYTES (see maxBodyCeiling).
	defaultMaxBodyCeiling = 4 << 20 // 4 MiB
	// shutdownTimeout bounds graceful drain on SIGTERM/SIGINT.
	shutdownTimeout = 15 * time.Second
	// devIngestKey is the fake dev key kept working in both modes: it maps to
	// devTenant via the static resolver, and is seeded into devOrgName when a
	// control plane is present.
	devIngestKey = "dev-key"
	devTenant    = "dev-tenant"
	devOrgName   = "dev-org"
)

// App is the built composition root: the fully-assembled HTTP mux plus the
// resources Serve runs and shutdown releases. build performs every wiring
// decision (infra construction + assembleMux); Serve runs the listener until a
// signal and drains gracefully. The split lets a test assert the wiring (via
// assembleMux) with fakes, without binding a socket or a live ClickHouse.
type App struct {
	log        *slog.Logger
	mux        http.Handler
	writer     *storage.Writer
	closeStore func()
	ready      *atomic.Bool
	cancelBg   context.CancelFunc
	listen     string
}

// Run builds the collector and serves it until a shutdown signal, then drains
// gracefully. It is the whole of what func main delegates to.
func Run(log *slog.Logger, opts ...Option) error {
	app, err := build(log, opts...)
	if err != nil {
		return err
	}
	return app.Serve()
}

// build constructs the App: it opens the storage writer, applies the schema,
// resolves the ingest wiring and control-plane-dependent collaborators, and hands
// them to assembleMux to produce the routed handler. Every infra dependency
// (ClickHouse, Postgres) is touched here; the routing decisions live in
// assembleMux so they stay unit-testable with fakes.
func build(log *slog.Logger, opts ...Option) (*App, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}

	startCtx, cancelStart := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelStart()

	writer, err := storage.NewWriter(startCtx, storage.ConfigFromEnv(), log)
	if err != nil {
		return nil, err
	}

	// Apply the ClickHouse schema at boot (idempotent DDL). Without this a fresh
	// ClickHouse has no summaries table, so both ingest and the dashboard fail.
	if err := writer.Migrate(startCtx, log); err != nil {
		return nil, err
	}

	wiring, err := buildIngestWiring(startCtx, log, o.limitProvider, o.migrations)
	if err != nil {
		return nil, err
	}
	// closeStore releases the control-plane pool (no-op in static dev mode); it is
	// called on every early-return below and once at shutdown.
	store := wiring.store
	closeStore := func() {
		if store != nil {
			store.Close()
		}
	}
	// The writer is both the summary RowSink and the per-instance USE-gauge sink;
	// wire the latter explicitly so instance windows are ingested into their table.
	ingestOpts := append(wiring.opts, ingest.WithInstanceWindowSink(writer))
	ingestHandler := ingest.NewHandler(wiring.resolver, writer, log, ingestOpts...)

	// The deployment origin (embedded in issued keys) and the HMAC key that signs
	// both session cookies and the Setup form CSRF tokens are resolved once and
	// shared by the auth layer and the dashboard. Both the auth member store and
	// the narrow dashboard control-plane surface come from the same concrete
	// store; assigning through the concrete non-nil check avoids a typed-nil
	// interface that would defeat the nil checks downstream.
	baseURL := os.Getenv("MAPING_BASE_URL")
	cpd, err := resolveControlPlane(store, o, baseURL, log)
	if err != nil {
		closeStore()
		return nil, err
	}

	mux, ready, cancelBg, err := assembleMux(builtDeps{
		querier:       scopedQuerier{qs: writer.QueryService()},
		ingestHandler: ingestHandler,
		cp:            cpd.cp,
		memberStore:   cpd.memberStore,
		interceptor:   cpd.interceptor,
		memberAdmin:   cpd.memberAdmin,
		pool:          cpd.pool,
		card:          wiring.card,
		constTenant:   wiring.tenant,
		baseURL:       baseURL,
		sessKey:       cpd.sessKey,
	}, o, log)
	if err != nil {
		closeStore()
		return nil, err
	}

	return &App{
		log:        log,
		mux:        mux,
		writer:     writer,
		closeStore: closeStore,
		ready:      ready,
		cancelBg:   cancelBg,
		listen:     os.Getenv("MAPING_LISTEN"),
	}, nil
}

// Serve runs the HTTP listener until SIGINT/SIGTERM, then drains gracefully:
// mark not-ready, stop accepting, drain the ingest->ClickHouse batcher (final
// flush with bounded retry) before releasing the control-plane pool so the last
// buffered summaries are not lost on a deploy restart, and cancel background jobs.
func (a *App) Serve() error {
	defer a.cancelBg()

	srv := newHTTPServer(a.listen, a.mux)

	errCh := make(chan error, 1)
	go func() {
		a.log.Info("listening", slog.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return err
	case sig := <-sigCh:
		a.log.Info("shutdown signal received", slog.String("signal", sig.String()))
	}

	a.ready.Store(false)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		a.log.Error("http shutdown", slog.Any("err", err))
	}
	if err := a.writer.Close(shutdownCtx); err != nil {
		a.log.Error("writer drain", slog.Any("err", err))
	}
	a.closeStore()
	a.log.Info("shutdown complete")
	return nil
}

// healthHandler serves the readiness probe: 200 while serving, 503 once shutdown
// has flipped ready to false so load balancers stop routing.
func healthHandler(ready *atomic.Bool) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if !ready.Load() {
			http.Error(w, "shutting down", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}
}

// mountDashboard attaches the dashboard at "/". When auth is on, the web routes
// live on a sub-mux wrapped by auth.Middleware while the open login routes
// register directly on the outer mux — Go 1.22 ServeMux precedence makes those
// more-specific patterns bypass the gate. "/" itself dispatches: when a public
// home is wired (a composing build's landing page) an anonymous visitor sees
// it and a signed-in one gets the dashboard; when it is nil (self-host/OSS/dev)
// anonymous "/" redirects to /login. Either way "/" is a bare (method-less)
// pattern so it does not conflict with the more-specific ingest path.
func mountDashboard(mux *http.ServeMux, webHandler *web.Handler, home http.HandlerFunc, authLayer *auth.Auth, log *slog.Logger) {
	webMux := http.NewServeMux()
	webHandler.Register(webMux)
	if authLayer == nil {
		mux.Handle("/", webMux)
		return
	}
	gated := authLayer.Middleware(webMux)
	if home != nil {
		// Public home wired: anonymous "/" serves it; signed-in users and every
		// sub-path fall through to the gated dashboard. Companion routes are
		// registered by the composing build via WithRoutes.
		mux.Handle("/", rootHandler(authLayer, gated, home))
	} else {
		// No public home: "/" is the gated dashboard (anonymous -> /login).
		mux.Handle("/", gated)
	}
	authLayer.Register(mux)
	providers, devLogin := authLayer.Enabled()
	log.Info("dashboard auth enabled",
		slog.Any("providers", providers), slog.Bool("dev_login", devLogin), slog.Bool("public_home", home != nil))
}

// rootHandler serves the injected public home to anonymous visitors at exactly
// "/", and routes everything else — signed-in users and every dashboard sub-path —
// to the gated dashboard (which still redirects an anonymous non-root request to
// /login). This is the anon-home / authed-app split at the site root.
func rootHandler(authLayer *auth.Auth, gated http.Handler, home http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" && !authLayer.Authenticated(r) {
			home(w, r)
			return
		}
		gated.ServeHTTP(w, r)
	})
}

// mountIngest attaches the body-capped Connect/gRPC ingest handler. The zstd
// codec (ADR-0002 wire contract) is registered so zstd-compressed client uploads
// decode; without it the server rejects them.
func mountIngest(mux *http.ServeMux, handler mapingv1connect.IngestServiceHandler, bodyCap int64) {
	path, connectHandler := mapingv1connect.NewIngestServiceHandler(handler, mapingcompress.HandlerOption())
	mux.Handle(path, withMaxBody(connectHandler, bodyCap))
}

// newHTTPServer builds the listener with h2c (unencrypted HTTP/2) enabled via the
// standard Protocols field so the client's cleartext gRPC works locally without
// TLS. An empty listen address defaults to :8080.
func newHTTPServer(listen string, mux http.Handler) *http.Server {
	if listen == "" {
		listen = ":8080"
	}
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	return &http.Server{
		Addr:    listen,
		Handler: mux,
		// ReadHeaderTimeout bounds slow-header (Slowloris) attacks; IdleTimeout
		// reaps idle keep-alive connections. ReadTimeout/WriteTimeout are left
		// unset deliberately: this listener multiplexes long-lived h2c/gRPC
		// (Connect) ingest connections, which a whole-request Read/Write deadline
		// would sever. Body size is bounded by MaxBytesReader and work by
		// per-request context deadlines instead.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		Protocols:         protocols,
	}
}

// ingestWiring is the resolved ingest auth + guardrail setup: the key resolver,
// the optional control-plane store (nil in static dev mode), the handler options
// that enable plan-driven rate limiting and the cardinality cap, the tenant the
// dashboard renders, and the cardinality guard (surfaced so the dashboard can
// show the per-tenant frozen state). store and card are nil in static dev mode,
// keeping dev-without-Postgres fully functional.
type ingestWiring struct {
	resolver ingest.KeyResolver
	store    *control.Store         // nil when no control plane is configured.
	card     *guardrail.Cardinality // nil when no control plane is configured.
	opts     []ingest.Option
	tenant   string
}

// buildIngestWiring wires the ingest path against the control plane when
// MAPING_POSTGRES_DSN is set (real key resolution + plan-driven guardrails), and
// falls back to the static dev-key resolver with default guardrails otherwise so
// local dev and the existing tests need no Postgres.
func buildIngestWiring(ctx context.Context, log *slog.Logger, limitFactory LimitProviderFactory, migrations []migrationSource) (ingestWiring, error) {
	pgDSN := os.Getenv("MAPING_POSTGRES_DSN")
	if pgDSN == "" {
		log.Warn("no control plane (MAPING_POSTGRES_DSN unset): using static dev-key resolver and default guardrails")
		return ingestWiring{
			resolver: ingest.NewStaticKeyResolver(map[string]string{devIngestKey: devTenant}),
			tenant:   devTenant,
		}, nil
	}

	// A composing build's extra migration sources apply after the embedded core
	// migrations, layering the build's own schema on top of the core without
	// the core carrying it.
	ctrlOpts := make([]control.Option, 0, len(migrations))
	for _, m := range migrations {
		ctrlOpts = append(ctrlOpts, control.WithExtraMigrations(m.fsys, m.dir))
	}
	store, err := control.New(ctx, pgDSN, ctrlOpts...)
	if err != nil {
		return ingestWiring{}, fmt.Errorf("control plane: %w", err)
	}
	// Seed the dev key so local dev keeps working against a real
	// control plane; idempotent. tenant is the seeded org id, which is what the
	// resolver returns and what the dashboard must query.
	tenant, err := store.EnsureDevKey(ctx, devIngestKey, devOrgName)
	if err != nil {
		store.Close()
		return ingestWiring{}, fmt.Errorf("seed dev key: %w", err)
	}

	// The core provider resolves the plain plan budget. A composing build
	// decorates it via WithLimitProvider (layering its own limit policy);
	// the public default leaves it untouched. Every ingest
	// guardrail resolves through this single provider so the decorator applies
	// uniformly to rate, cardinality, and payload.
	var provider guardrail.LimitProvider = limitProvider{store: store}
	if limitFactory != nil {
		provider = limitFactory(provider, store.Pool())
	}
	// The TTL cache wraps the WHOLE chain (core provider plus any decorator) so
	// a decorator's own queries are amortized too; the staleness bound for plan
	// changes and suspend decisions is the cache TTL.
	provider = guardrail.NewCachedLimitProvider(provider)
	rl := guardrail.NewRateLimiter(provider)
	card := guardrail.NewCardinality()

	opts := []ingest.Option{
		// The token-bucket check is per request and fast; a background context is
		// fine and avoids coupling the limiter to a request deadline.
		//nolint:contextcheck // intentional detached context: the limiter must not inherit a request deadline.
		ingest.WithLimiter(func(tenant string) bool { return rl.Allow(context.Background(), tenant) }),
		ingest.WithCardinality(card.Allow, func(ctx context.Context, tenant string) int {
			l, _ := provider.Limits(ctx, tenant)
			return l.CardinalityCap
		}),
		// Enforce the plan's max_payload_bytes as the logical/fairness bound
		// after auth; the HTTP-layer ceiling above is the hard
		// pre-auth memory bound. Fed from the same control-plane limits.
		ingest.WithPayloadLimit(func(ctx context.Context, tenant string) int64 {
			l, _ := provider.Limits(ctx, tenant)
			return l.MaxPayloadBytes
		}),
		// Persist handshakes so the dashboard onboarding panel shows connected
		// services. The store is adapted to ingest.HandshakeRecorder; a control-
		// plane write failure never fails the handshake (see ingest.Register).
		ingest.WithHandshakeRecorder(store),
	}
	return ingestWiring{
		resolver: control.NewResolver(store.Pool(), log),
		store:    store,
		card:     card,
		opts:     opts,
		tenant:   tenant,
	}, nil
}

// limitProvider adapts *control.Store to guardrail.LimitProvider: the store's
// Limits returns an error, which the provider maps to ok=false so the rate
// limiter falls back to DefaultLimits rather than failing ingest on a lookup
// error.
type limitProvider struct{ store *control.Store }

func (p limitProvider) Limits(ctx context.Context, tenant string) (guardrail.Limits, bool) {
	l, err := p.store.Limits(ctx, tenant)
	if err != nil {
		return guardrail.Limits{}, false
	}
	return l, true
}

// withMaxBody caps the request body size for the wrapped handler.
func withMaxBody(next http.Handler, limit int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next.ServeHTTP(w, r)
	})
}
