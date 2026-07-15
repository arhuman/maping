package ingest

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/arhuman/maping/server/internal/guardrail"
	"github.com/arhuman/maping/server/internal/storage"
	"github.com/arhuman/maping/server/internal/tenant"

	mapingv1 "github.com/arhuman/maping/proto/maping/v1"
	"github.com/arhuman/maping/proto/maping/v1/mapingv1connect"
)

// RowSink is the write side the handler depends on, so the handler is testable
// with a fake sink and does not import a live ClickHouse connection. The
// storage.Writer satisfies this via Enqueue.
type RowSink interface {
	// Enqueue hands one converted Summary row to the data plane.
	Enqueue(row storage.Row) error
}

// InstanceWindowSink is the optional write side for per-instance USE gauges. It
// is separate from RowSink (a different table) and wired only when a sink is
// provided (WithInstanceWindowSink), so the zero-option handler and existing
// tests ignore instance windows entirely. The storage.Writer satisfies it via
// EnqueueInstanceWindow.
type InstanceWindowSink interface {
	EnqueueInstanceWindow(row storage.InstanceWindowRow) error
}

// Auth header conventions. The ingest key arrives either as a bearer token or
// in the mAPI-ng-specific header.
const (
	headerAuthorization = "Authorization"
	headerMapingKey     = "X-Maping-Key"
	bearerPrefix        = "Bearer "
)

// Default rate-limit guardrail applied when no per-tenant limiter is wired.
const (
	defaultTenantRPS   = 100.0
	defaultTenantBurst = 200
)

// nowFunc is injectable for deterministic timestamp-policy tests.
type nowFunc func() time.Time

// rateLimiter is the throttling contract the handler depends on: report whether
// a request for tenant may proceed. The default *tenantLimiter satisfies it,
// so tests that assign h.limiter = newTenantLimiter(...) still compile;
// guardrail.RateLimiter is adapted to it at wiring time.
type rateLimiter interface {
	allow(tenant string) bool
}

// allowFunc adapts a plain func into a rateLimiter, so main can supply the
// plan-driven guardrail.RateLimiter without exposing its type here.
type allowFunc func(tenant string) bool

func (f allowFunc) allow(tenant string) bool { return f(tenant) }

// cardinalityFunc is the optional best-effort series-cardinality cap: report
// whether the series may be ingested for tenant under cap and whether the tenant
// is currently frozen. Nil by default (no guard), so current behavior and tests
// are unchanged; main adapts guardrail.Cardinality.Allow into it.
type cardinalityFunc func(tenant, seriesKey string, capacity int) (allowed, frozen bool)

// capProvider resolves a tenant's cardinality cap for the guard. Supplied
// alongside WithCardinality.
type capProvider func(ctx context.Context, tenant string) int

// payloadLimitFunc resolves a tenant's logical max ingest payload size in bytes,
// enforcing the per-plan max_payload_bytes. It is the fairness bound,
// applied to the decoded request AFTER auth (the tenant is only known then); the
// fixed HTTP-layer body cap remains as a pre-auth memory-safety ceiling. Nil by
// default (no per-tenant check), so dev-without-Postgres and existing tests are
// unchanged. A returned value <= 0 disables the check for that tenant, mirroring
// the cardinality cap<=0 convention.
type payloadLimitFunc func(ctx context.Context, tenant string) int64

// HandshakeRecorder persists the one-time registration ping so the dashboard's
// onboarding panel can show "service connected" (CONTEXT Handshake). It is an
// interface, not a *control.Store, so ingest stays control-agnostic (ingest must
// never import control — main adapts the store to this at wiring time). A nil
// recorder (the default) keeps today's log-only Register behavior.
type HandshakeRecorder interface {
	RecordHandshake(ctx context.Context, tenant, service, instance, sdkVersion string) error
}

// Handler implements mapingv1connect.IngestServiceHandler. It authenticates the
// ingest key, resolves the tenant, applies the timestamp policy, converts each
// Summary to a storage row, and enqueues it. Unknown keys are rejected with
// CodeUnauthenticated; per-tenant abuse is throttled with a token bucket.
type Handler struct {
	resolver KeyResolver
	sink     RowSink
	limiter  rateLimiter
	log      *slog.Logger
	now      nowFunc

	cardinality cardinalityFunc
	cap         capProvider

	payloadLimit payloadLimitFunc

	handshakes HandshakeRecorder

	iwSink InstanceWindowSink
}

var _ mapingv1connect.IngestServiceHandler = (*Handler)(nil)

// Option configures an ingest Handler. Options are additive: the zero-option
// call reproduces the fixed default limiter and no cardinality guard,
// so existing wiring and tests are unaffected.
type Option func(*Handler)

// WithLimiter replaces the default per-tenant token-bucket with a caller-
// supplied allow function (e.g. adapting guardrail.RateLimiter fed by
// plan_limits). A nil allow is ignored, keeping the default.
func WithLimiter(allow func(tenant string) bool) Option {
	return func(h *Handler) {
		if allow != nil {
			h.limiter = allowFunc(allow)
		}
	}
}

// WithCardinality enables the best-effort series-cardinality cap. allow tracks
// per-tenant series (adapting guardrail.Cardinality.Allow); cap resolves each
// tenant's budget (typically from the control plane). Both must be non-nil for
// the guard to run.
func WithCardinality(allow cardinalityFunc, capacity capProvider) Option {
	return func(h *Handler) {
		h.cardinality = allow
		h.cap = capacity
	}
}

// WithPayloadLimit enables per-tenant logical payload enforcement. cap resolves
// the tenant's max ingest payload in bytes (typically from the
// control plane); Upload measures the decoded request with proto.Size and
// rejects the whole request with CodeResourceExhausted when it exceeds the cap.
// A nil cap or a cap returning <= 0 disables the check, so the option is purely
// additive: absent it (dev-without-Postgres / existing tests), no per-tenant
// payload check runs and behavior is unchanged.
func WithPayloadLimit(limit func(ctx context.Context, tenant string) int64) Option {
	return func(h *Handler) {
		if limit != nil {
			h.payloadLimit = limit
		}
	}
}

// WithHandshakeRecorder persists each accepted Register handshake through r, so
// the dashboard onboarding panel reflects connected services. A nil r is
// ignored, keeping the default log-only behavior and leaving existing tests
// unchanged. Recording failures never fail the handshake (see Register).
func WithHandshakeRecorder(r HandshakeRecorder) Option {
	return func(h *Handler) {
		if r != nil {
			h.handshakes = r
		}
	}
}

// WithInstanceWindowSink enables ingestion of per-instance USE gauges through
// sink (typically the storage.Writer). A nil sink is ignored, keeping instance
// windows off by default so existing wiring and tests are unaffected.
func WithInstanceWindowSink(sink InstanceWindowSink) Option {
	return func(h *Handler) {
		if sink != nil {
			h.iwSink = sink
		}
	}
}

// NewHandler builds an ingest Handler. Without options it uses the fixed default
// per-tenant limiter and no cardinality guard.
func NewHandler(resolver KeyResolver, sink RowSink, log *slog.Logger, opts ...Option) *Handler {
	if log == nil {
		log = slog.Default()
	}
	h := &Handler{
		resolver: resolver,
		sink:     sink,
		limiter:  newTenantLimiter(defaultTenantRPS, defaultTenantBurst),
		log:      log,
		now:      func() time.Time { return time.Now().UTC() },
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// extractKey reads the ingest key from the request headers, preferring the
// bearer Authorization header and falling back to X-Maping-Key.
func extractKey(h interface{ Get(string) string }) string {
	if auth := h.Get(headerAuthorization); strings.HasPrefix(auth, bearerPrefix) {
		return strings.TrimSpace(strings.TrimPrefix(auth, bearerPrefix))
	}
	return strings.TrimSpace(h.Get(headerMapingKey))
}

// authenticate resolves the tenant from the request's ingest key, returning a
// CodeUnauthenticated error for a missing or unknown key. The resolved id is
// parsed once into a tenant.ID here at the boundary, so everything downstream
// (guards, conversion, the stored Row) receives a validated identity rather
// than a bare string — a cross-tenant or empty-tenant write is unrepresentable.
func (h *Handler) authenticate(ctx context.Context, headerGetter interface{ Get(string) string }) (tenant.ID, error) {
	key := extractKey(headerGetter)
	if key == "" {
		return tenant.ID{}, connect.NewError(connect.CodeUnauthenticated, errors.New("missing ingest key"))
	}
	resolved, ok := h.resolver.Resolve(ctx, key)
	if !ok {
		return tenant.ID{}, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid ingest key"))
	}
	tid, err := tenant.Parse(resolved)
	if err != nil {
		// A resolver returning an empty tenant is a control-plane fault, not a
		// valid identity: refuse rather than write an unscoped row.
		return tenant.ID{}, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid ingest key"))
	}
	return tid, nil
}

// Register records a handshake: it authenticates and returns accepted,
// driving the dashboard onboarding state (CONTEXT Handshake).
func (h *Handler) Register(
	ctx context.Context,
	req *connect.Request[mapingv1.Handshake],
) (*connect.Response[mapingv1.RegisterResponse], error) {
	tid, err := h.authenticate(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	service := req.Msg.GetService()
	instance := req.Msg.GetInstance()
	sdkVersion := req.Msg.GetSdkVersion()

	h.log.Info("handshake accepted",
		slog.String("tenant", tid.String()),
		slog.String("service", service),
		slog.String("instance", instance),
		slog.String("sdk_version", sdkVersion),
	)

	// Persist the handshake for the onboarding panel when a recorder is wired.
	// Log-and-continue on error: the ping's job is proving auth + connectivity,
	// so a control-plane write failure must never turn a valid handshake into a
	// client-visible failure (CONTEXT Handshake; data fails open, setup is loud
	// but not fatal).
	if h.handshakes != nil {
		if err := h.handshakes.RecordHandshake(ctx, tid.String(), service, instance, sdkVersion); err != nil {
			h.log.Error("handshake record failed",
				slog.String("tenant", tid.String()),
				slog.String("service", service),
				slog.String("instance", instance),
				slog.Any("err", err),
			)
		}
	}

	return connect.NewResponse(&mapingv1.RegisterResponse{Accepted: true}), nil
}

// Upload validates and stores a batch of Summaries. Out-of-band-skew summaries
// are dropped and counted into RejectedSummaries rather than clamped onto now.
func (h *Handler) Upload(
	ctx context.Context,
	req *connect.Request[mapingv1.UploadRequest],
) (*connect.Response[mapingv1.UploadResponse], error) {
	tid, err := h.authenticate(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	if !h.limiter.allow(tid.String()) {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("rate limit exceeded"))
	}

	// Per-tenant logical payload enforcement. The fixed HTTP-layer body cap is a
	// pre-auth memory-safety ceiling; the plan's MaxPayloadBytes is the fairness
	// bound, enforceable only here because the tenant is now known. Fail closed
	// with a reason the client can log: reject the whole request, never truncate.
	if !h.payloadWithinLimit(ctx, tid, req.Msg) {
		return nil, connect.NewError(connect.CodeResourceExhausted,
			errors.New("payload exceeds per-tenant max_payload_bytes"))
	}

	env := req.Msg.GetEnvelope()
	service := env.GetService()
	instance := env.GetInstance()
	// Deploy identity travels on the Envelope and applies to every summary in the
	// batch. It is stored as a low-cardinality dimension, not part of any series
	// or cardinality key.
	deploy := deployIdentity{
		version:     env.GetDeployVersion(),
		id:          env.GetDeployId(),
		environment: env.GetEnvironment(),
		region:      env.GetRegion(),
		start:       instanceStartTime(env.GetInstanceStartTimeMs()),
	}
	now := h.now()

	// Resolve the tenant's cardinality cap once per request; the guard is
	// best-effort per-node (see guardrail.Cardinality).
	var cardCap int
	if h.cardinality != nil && h.cap != nil {
		cardCap = h.cap(ctx, tid.String())
	}

	rejected := h.storeSummaries(tid, service, instance, deploy, req.Msg.GetSummaries(), now, cardCap)

	// Per-instance USE gauges ride along on the same upload but land in a separate
	// table (best-effort; a closed/backpressured USE stream must never fail the RED
	// upload).
	h.storeInstanceWindows(tid, service, instance, req.Msg.GetInstanceWindows(), now)

	return connect.NewResponse(&mapingv1.UploadResponse{
		Accepted:          true,
		RejectedSummaries: rejected,
	}), nil
}

// deployIdentity is the per-batch deploy dimension carried on the Envelope. It
// applies to every summary in the batch and is stored as a low-cardinality
// dimension — deliberately NOT part of the series or cardinality key.
type deployIdentity struct {
	version     string
	id          string
	environment string
	region      string
	start       time.Time
}

// instanceStartTime converts the Envelope's instance_start_time_ms into a UTC
// time, mapping the proto3 zero (unset) to the zero time rather than the epoch.
func instanceStartTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

// storeSummary validates one summary against the timestamp and cardinality
// guards, converts it to a row, and enqueues it. It returns false when the
// summary is dropped for any reason — out-of-band skew, a frozen new series, a
// conversion error, or a backpressured/closed data plane — so Upload can simply
// count rejections without unwinding the guard sequence inline.
func (h *Handler) storeSummary(tid tenant.ID, service, instance string, deploy deployIdentity, s *mapingv1.Summary, now time.Time, cardCap int) bool {
	decision := applyTimestampPolicy(s.GetWindowStartMs(), s.GetWindowEndMs(), now)
	if !decision.accepted {
		return false
	}
	if h.cardinality != nil {
		seriesKey := guardrail.SeriesKey(s.GetMethod(), s.GetRouteTemplate(), statusClassName(s.GetStatusClass()))
		if allowed, _ := h.cardinality(tid.String(), seriesKey, cardCap); !allowed {
			// New series beyond the tenant's cap: freeze it (reject and count)
			// while existing series keep flowing.
			return false
		}
	}
	row, err := summaryToRow(tid, service, instance,
		deploy.version, deploy.id, deploy.environment, deploy.region, deploy.start,
		s, decision.start, decision.end)
	if err != nil {
		return false
	}
	// Enqueue failure means the data plane refused (closed/backpressured): count
	// as rejected so the client sees the drop rather than a whole-batch failure.
	return h.sink.Enqueue(row) == nil
}

// payloadWithinLimit reports whether req fits the tenant's plan payload cap. A
// nil provider or a cap <= 0 disables the check (returns true); otherwise the
// decoded proto size must not exceed the cap.
func (h *Handler) payloadWithinLimit(ctx context.Context, tid tenant.ID, req *mapingv1.UploadRequest) bool {
	if h.payloadLimit == nil {
		return true
	}
	limit := h.payloadLimit(ctx, tid.String())
	if limit <= 0 {
		return true
	}
	return int64(proto.Size(req)) <= limit
}

// storeSummaries stores each summary in the batch and returns how many were
// rejected (out-of-band skew, a frozen new series, a conversion error, or a
// backpressured/closed data plane — see storeSummary).
func (h *Handler) storeSummaries(tid tenant.ID, service, instance string, deploy deployIdentity, summaries []*mapingv1.Summary, now time.Time, cardCap int) uint64 {
	var rejected uint64
	for _, s := range summaries {
		if !h.storeSummary(tid, service, instance, deploy, s, now, cardCap) {
			rejected++
		}
	}
	return rejected
}

// storeInstanceWindows drains the batch's per-instance USE gauges into the
// instance_windows stream. Best-effort: a closed/backpressured USE stream must
// never fail the RED upload, so the first enqueue error just stops the drain.
func (h *Handler) storeInstanceWindows(tid tenant.ID, service, instance string, windows []*mapingv1.InstanceWindow, now time.Time) {
	if h.iwSink == nil {
		return
	}
	for _, iw := range windows {
		row, ok := instanceWindowToRow(tid, service, instance, iw, now)
		if !ok {
			continue
		}
		if err := h.iwSink.EnqueueInstanceWindow(row); err != nil {
			return
		}
	}
}
