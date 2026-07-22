package app

import (
	"context"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arhuman/maping/server/docs"
)

// The extension seam: a composed build (a separate module that imports
// server/app) adds HTTP surfaces and background work through Run's
// functional options rather than by editing the core wiring. Every exported type
// here carries only externally-nameable types (stdlib + pgx), so a registrar can
// be implemented from another module that cannot import server/internal/*.

// RouteContext is what a WithRoutes registrar receives to mount extra routes.
// Pool is the control-plane pool, nil in static dev mode (no MAPING_POSTGRES_DSN).
type RouteContext struct {
	Mux  *http.ServeMux
	Pool *pgxpool.Pool
	Log  *slog.Logger
	// Gate wraps a handler in the dashboard auth middleware (session-cookie
	// verification, redirect/401 on failure) so an extension can mount its own
	// authenticated routes with the same gate the dashboard uses. It is nil when
	// auth is off (no control plane) — a registrar that needs auth must check and
	// mount nothing.
	Gate func(http.Handler) http.Handler
	// SessionOrg reads the caller's verified org id from a request that has passed
	// through Gate. It is the ONLY sanctioned way for an extension to learn the
	// caller's org (never the request body), keeping the session context key
	// private to auth. Nil when auth is off.
	SessionOrg func(*http.Request) (orgID string, ok bool)
	// SessionMemberID reads the caller's verified member id from a request that has
	// passed through Gate. Like SessionOrg it reads the private auth session context
	// key an extension cannot reach directly, so an extension can resolve the caller's
	// own member row (e.g. their email) without trusting request input. Nil when auth
	// is off.
	SessionMemberID func(*http.Request) (memberID string, ok bool)
	// RenderShell writes a full dashboard page (sidebar + top bar chrome) wrapping
	// the given content, so an extension's own page looks native to the dashboard
	// instead of a detached page. content is trusted HTML the caller produced from a
	// template; title sets the top-bar heading and browser tab. Always non-nil.
	RenderShell func(w http.ResponseWriter, r *http.Request, title string, content template.HTML)
	// RenderDoc writes a full public documentation page (the /doc dark shell with the
	// shared table of contents in the left rail) wrapping the given body, so an
	// extension's own doc page is indistinguishable from a core one. body is trusted
	// HTML — a composing build produces it from its embedded Markdown via
	// docs.MarkdownToHTML. Unlike RenderShell the page is public (no auth chrome), so
	// this is the seam for extension /doc/* pages. Always non-nil.
	RenderDoc func(w http.ResponseWriter, r *http.Request, title string, body template.HTML)
}

// JobContext is what a WithBackgroundJob task receives. Ctx is cancelled at
// shutdown, so a well-behaved job runs until Ctx.Done(). Pool is nil in dev mode.
type JobContext struct {
	Ctx  context.Context
	Pool *pgxpool.Pool
	Log  *slog.Logger
}

// RouteRegistrar mounts additional routes on the server mux. It runs after the
// core routes, so its patterns must not collide with the core surfaces
// (/healthz, /, the /login and /auth routes, and the ingest path).
type RouteRegistrar func(RouteContext)

// BackgroundJob is a long-running task started after boot and stopped at
// shutdown via JobContext.Ctx.
type BackgroundJob func(JobContext)

// LimitProviderFactory decorates the core LimitProvider with the injecting
// build's own behavior — e.g. an account lifecycle (suspend/trial) the
// composing build reads from its own schema. base is the plain plan-budget provider; pool is the
// control-plane pool (never nil when this factory runs, since limits require a
// control plane). It returns the provider the ingest guardrails resolve through.
// It names the public LimitProvider alias so a composing module (which cannot
// import server/internal/guardrail) can implement it.
type LimitProviderFactory func(base LimitProvider, pool *pgxpool.Pool) LimitProvider

// migrationSource is a composing build's extra migration directory, stored as its
// externally-nameable (fs.FS + dir) parts rather than the internal
// control.MigrationSource so WithExtraMigrations exposes no internal type.
type migrationSource struct {
	fsys fs.FS
	dir  string
}

// LoginInterceptorFactory builds the composed post-auth hook once the control-
// plane pool exists (the invite store the hook drives needs it). pool is never nil
// when this runs, since the hook is only constructed with a control plane.
type LoginInterceptorFactory func(pool *pgxpool.Pool) LoginInterceptor

// MemberAdminFactory builds the team-panel admin once the control-plane pool
// exists. pool is never nil when this runs.
type MemberAdminFactory func(pool *pgxpool.Pool) MemberAdmin

// options holds the functional-option configuration for Run.
type options struct {
	routes           []RouteRegistrar
	jobs             []BackgroundJob
	limitProvider    LimitProviderFactory
	migrations       []migrationSource
	loginInterceptor LoginInterceptorFactory
	memberAdmin      MemberAdminFactory
	publicHome       http.HandlerFunc
	accountHref      string
	docSections      []docs.Section
	docHeader        template.HTML
}

// Option configures Run.
type Option func(*options)

// WithRoutes registers a RouteRegistrar mounted after the core routes. Multiple
// registrars run in registration order.
func WithRoutes(r RouteRegistrar) Option {
	return func(o *options) { o.routes = append(o.routes, r) }
}

// WithBackgroundJob registers a task launched after boot in its own goroutine
// and cancelled at shutdown. Multiple jobs each get their own goroutine.
func WithBackgroundJob(j BackgroundJob) Option {
	return func(o *options) { o.jobs = append(o.jobs, j) }
}

// WithExtraMigrations registers an additional control-plane migration source
// applied, in lexical filename order, AFTER the embedded core migrations. A
// composing build passes its own schema (its own plan rows and tables) here so
// the public core never carries it. Multiple sources apply in registration order.
// Files must be additive and idempotent, exactly like the core migrations. It has
// no effect in static dev mode (no control plane to migrate).
func WithExtraMigrations(fsys fs.FS, dir string) Option {
	return func(o *options) { o.migrations = append(o.migrations, migrationSource{fsys: fsys, dir: dir}) }
}

// WithLimitProvider decorates the core LimitProvider that drives the ingest
// guardrails (rate, payload, cardinality). The composing build passes
// its own provider to layer its own limit policy on top; the public
// default (no option) resolves the plain plan budget. It has no effect in static
// dev mode, where there is no control plane to resolve limits.
func WithLimitProvider(factory LimitProviderFactory) Option {
	return func(o *options) { o.limitProvider = factory }
}

// WithLoginInterceptor wires a post-authentication hook (e.g. an invitation
// accept flow) the OIDC callback consults before the default first-login path.
// The factory receives the control-plane pool the hook's store needs. Public
// default: none (plain login). No effect in static dev mode (no control plane).
func WithLoginInterceptor(factory LoginInterceptorFactory) Option {
	return func(o *options) { o.loginInterceptor = factory }
}

// WithMemberAdmin wires the self-serve team panel (members + invites) the Setup
// page renders. The factory receives the control-plane pool the admin's store
// needs. Public default: nil, so the dashboard hides the team panel. No effect in
// static dev mode (no control plane).
func WithMemberAdmin(factory MemberAdminFactory) Option {
	return func(o *options) { o.memberAdmin = factory }
}

// WithPublicHome wires the anonymous landing page served at "/". When set, an
// unauthenticated visitor to "/" gets this handler while
// signed-in users and every dashboard sub-path fall through to the gated
// dashboard; a composing build registers any companion routes via
// WithRoutes. Public default: nil, so anonymous "/" redirects to /login (the
// self-host/OSS surface serves no landing page).
func WithPublicHome(home http.HandlerFunc) Option {
	return func(o *options) { o.publicHome = home }
}

// WithDocSections adds entries to the shared /doc table of contents (the left
// rail on every documentation page). A composing build registers its own doc
// topics here so they appear alongside the core product pages; it mounts the pages
// themselves via WithRoutes, rendering each through RouteContext.RenderDoc. Public
// default: none, so the community build shows only the core product sections.
func WithDocSections(sections ...docs.Section) Option {
	return func(o *options) { o.docSections = append(o.docSections, sections...) }
}

// WithDocHeader injects a full site header rendered above every /doc page, so the
// documentation wears the same chrome as the rest of the site (the same logo, nav,
// and calls to action) instead of a detached bar. html is trusted markup the
// composing build produced from its own templates — typically its marketing header
// with absolute links. The community build sets none, and the shell falls back to
// a minimal home brand. Setting it more than once keeps the last header.
func WithDocHeader(html template.HTML) Option {
	return func(o *options) { o.docHeader = html }
}

// WithAccountLink turns the dashboard sidebar's user-identity block into a link to
// href — a composing build points it at the account page it owns (e.g. "/account").
// Public default: empty, so the block stays a non-interactive display element. The
// composing build should only set it when it actually mounts that route (e.g. gating
// on the control plane) so the link never points at a 404.
func WithAccountLink(href string) Option {
	return func(o *options) { o.accountHref = href }
}

// mountExtensions applies each WithRoutes registrar onto the server mux after the
// core routes are mounted. pool is nil in static dev mode; gate and sessionOrg are
// nil when auth is off, so a registrar needing authenticated routes checks them
// and mounts nothing when absent.
func mountExtensions(mux *http.ServeMux, routes []RouteRegistrar, pool *pgxpool.Pool, gate func(http.Handler) http.Handler, sessionOrg func(*http.Request) (string, bool), sessionMemberID func(*http.Request) (string, bool), renderShell func(http.ResponseWriter, *http.Request, string, template.HTML), renderDoc func(http.ResponseWriter, *http.Request, string, template.HTML), log *slog.Logger) {
	for _, r := range routes {
		r(RouteContext{Mux: mux, Pool: pool, Log: log, Gate: gate, SessionOrg: sessionOrg, SessionMemberID: sessionMemberID, RenderShell: renderShell, RenderDoc: renderDoc})
	}
	if len(routes) > 0 {
		log.Info("extension routes mounted", slog.Int("count", len(routes)))
	}
}

// startBackgroundJobs launches each WithBackgroundJob under ctx, which is
// cancelled at shutdown so the goroutines exit before the control-plane pool is
// closed.
func startBackgroundJobs(ctx context.Context, jobs []BackgroundJob, pool *pgxpool.Pool, log *slog.Logger) {
	for _, j := range jobs {
		go j(JobContext{Ctx: ctx, Pool: pool, Log: log})
	}
	if len(jobs) > 0 {
		log.Info("extension background jobs started", slog.Int("count", len(jobs)))
	}
}
