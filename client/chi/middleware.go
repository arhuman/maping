// Package chi is the mAPI-ng adapter for the go-chi/chi v5 router. Its only job
// is to extract, after each request completes, the two things the Core needs —
// the registered route pattern (never the raw path) and the final status code —
// and call the Recorder's Observe. Only this package imports chi, so a non-chi
// user never pulls chi into their binary.
//
// Registration order matters: register this middleware ABOVE chi's
// middleware.Recoverer (chi runs middleware in registration order, outer-first)
// so that when Recoverer turns a panic into a 500, this middleware simply
// observes that 500. The middleware deliberately does NOT recover panics itself
// — it observes host behavior, it never alters it.
//
// chi resolves the matched route pattern only AFTER the handler chain runs (a
// nested router contributes its mount prefix as the chain unwinds), so the
// template is read after next.ServeHTTP via chi.RouteContext().RoutePattern()
// (e.g. "/users/{id}", or the full combined pattern for nested routers).
// RoutePattern is already path-only, so no method/host stripping is needed. A
// request that matched no route, or one where this middleware runs outside a chi
// router (no RouteContext), reports an empty pattern and is skipped, so the raw
// path is never observed (which would explode series cardinality).
package chi

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	maping "github.com/arhuman/maping/client"
	"github.com/arhuman/maping/client/adapterutil"
)

// Middleware creates a Recorder from the given options and returns chi
// middleware that observes each request. With no ingest key resolved the
// Recorder is a no-op, so this is always safe to add (zero-config). The returned
// middleware does not expose the Recorder, so it cannot be shut down; use
// MiddlewareWithRecorder when the host needs to manage the lifecycle.
func Middleware(opts ...maping.Option) func(http.Handler) http.Handler {
	return MiddlewareWithRecorder(maping.NewRecorder(opts...))
}

// MiddlewareWithRecorder returns chi middleware bound to a caller-owned
// Recorder, so the host controls its lifecycle and can call Shutdown (after
// http.Server.Shutdown).
//
// No ErrorClass is set: chi has no error-attach mechanism (like stdlib
// net/http, unlike Gin's c.Error), so the adapter honestly leaves it empty.
func MiddlewareWithRecorder(rec *maping.Recorder) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Install the downstream-time accumulator so a maping.RoundTripper on the
			// host's outbound http.Client can attribute round-trip time to this
			// request. It is inert unless the host wires the RoundTripper and
			// propagates this context, so it costs a single context value otherwise.
			// Bind the context once (not inline) so the contextcheck linter sees a
			// single derived context, mirroring the nethttp adapter.
			ctx := maping.WithDownstreamTracking(r.Context())
			r = r.WithContext(ctx)

			cw := adapterutil.WrapResponseWriter(w)

			start := time.Now()
			next.ServeHTTP(cw, r)

			// chi fills the matched pattern as the handler chain unwinds, so read it
			// AFTER next.ServeHTTP. Guard nil: outside a chi router there is no
			// RouteContext and chi.RouteContext returns nil (must not panic).
			// Read from the bound ctx (not r.Context()): chi stores a single
			// *chi.Context pointer in the request context and fills its pattern in
			// place as routing resolves, so ctx observes the final value — and it
			// keeps contextcheck seeing one derived context.
			route := ""
			if rctx := chi.RouteContext(ctx); rctx != nil {
				route = rctx.RoutePattern()
			}
			if route == "" {
				// Unmatched route (or used outside a chi router): skip to avoid emitting
				// a raw path, which would explode series cardinality.
				return
			}

			status, reason := adapterutil.ReclassifyNoStatus(ctx, cw.Status())
			traceID, spanID := adapterutil.ParseTraceparent(r.Header.Get("traceparent"))

			rec.Observe(maping.Record{
				Method:             r.Method,
				RouteTemplate:      route,
				Status:             status,
				Duration:           time.Since(start),
				ReqBytes:           adapterutil.ClampNonNegative(r.ContentLength),
				RespBytes:          adapterutil.ClampNonNegative(cw.BytesWritten()),
				TraceID:            traceID,
				SpanID:             spanID,
				RequestID:          r.Header.Get("X-Request-Id"),
				NoStatusReason:     reason,
				DownstreamDuration: maping.DownstreamElapsed(ctx),
			})
		})
	}
}
