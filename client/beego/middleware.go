// Package beego is the mAPI-ng adapter for the Beego v2 web framework. Its only
// job is to extract, after each request completes, the two things the Core needs
// — the registered route template (never the raw path) and the final status code
// — and call the Recorder's Observe. Only this package imports Beego, so a
// non-Beego user never pulls Beego into their binary.
//
// Beego has no middleware chain like Gin/Echo/chi; the middleware-style primitive
// is a filter chain. Wire it once, matching every request:
//
//	web.InsertFilterChain("/*", mapingbeego.FilterWithRecorder(rec))
//
// A FilterChain wraps Beego's whole routing+dispatch step (serveHttp): it runs
// BEFORE routing, calls next(ctx) — which resolves the route, stores the matched
// pattern, and runs the controller — then regains control AFTER the response is
// written. That ordering is exactly what this adapter needs: the route template
// is only knowable post-routing, and the status is only final post-dispatch.
//
// Beego's panic recovery (defaultRecoverPanic) is deferred INSIDE serveHttp, so
// it sits inner to the filter chain: a panicking controller is converted to a
// 500 within next(ctx), and this filter simply observes that 500. The filter
// deliberately does NOT recover panics itself — it observes host behavior, it
// never alters it.
//
// The matched template is read from ctx.Input.GetData("RouterPattern") (e.g.
// "/users/:id"), the same key Beego's own prometheus filter uses. A request that
// matched no route (Beego's 404) has no stored pattern and is skipped, so the raw
// path is never observed (which would explode series cardinality).
//
// Beego's *context.Response exposes the final status but no written-byte count,
// so RespBytes is always reported as 0 for this adapter (an honest gap, not a
// fabricated value).
package beego

import (
	"net/http"
	"time"

	"github.com/beego/beego/v2/server/web"
	"github.com/beego/beego/v2/server/web/context"

	maping "github.com/arhuman/maping/client"
	"github.com/arhuman/maping/client/adapterutil"
)

// Filter creates a Recorder from the given options and returns a Beego filter
// chain that observes each request. With no ingest key resolved the Recorder is
// a no-op, so this is always safe to add (zero-config). The returned chain does
// not expose the Recorder, so it cannot be shut down; use FilterWithRecorder
// when the host needs to manage the lifecycle.
func Filter(opts ...maping.Option) web.FilterChain {
	return FilterWithRecorder(maping.NewRecorder(opts...))
}

// FilterWithRecorder returns a Beego filter chain bound to a caller-owned
// Recorder, so the host controls its lifecycle and can call Shutdown (after the
// server stops).
//
// No ErrorClass is set: Beego has no per-request error-attach mechanism (like
// stdlib net/http and chi, unlike Gin's c.Error), so the adapter honestly leaves
// it empty.
func FilterWithRecorder(rec *maping.Recorder) web.FilterChain {
	return func(next web.FilterFunc) web.FilterFunc {
		return func(ctx *context.Context) {
			// Install the downstream-time accumulator so a maping.RoundTripper on the
			// host's outbound http.Client can attribute round-trip time to this
			// request. It is inert unless the host wires the RoundTripper and
			// propagates this context, so it costs a single context value otherwise.
			// Beego threads the same *context.Context (and thus ctx.Request) through
			// routing to the controller, so replacing ctx.Request here propagates the
			// context downstream. Bind it once (not inline) so the contextcheck linter
			// sees a single derived context, mirroring the other adapters.
			reqCtx := maping.WithDownstreamTracking(ctx.Request.Context())
			ctx.Request = ctx.Request.WithContext(reqCtx)

			start := time.Now()
			next(ctx)

			// Beego stores the matched pattern only after routing (inside next), so
			// read it now. An unmatched route (Beego's 404) never stores it → skip to
			// avoid emitting a raw path, which would explode series cardinality.
			route, _ := ctx.Input.GetData("RouterPattern").(string)
			if route == "" {
				return
			}

			// A completed Beego response that never called WriteHeader leaves Status
			// at 0 (Response.Write does not set it); that is an implicit 200. Default
			// it BEFORE ReclassifyNoStatus so a genuine context abort can still
			// override it to 0.
			status := ctx.ResponseWriter.Status
			if status == 0 {
				status = http.StatusOK
			}

			status, reason := adapterutil.ReclassifyNoStatus(reqCtx, status)
			traceID, spanID := adapterutil.ParseTraceparent(ctx.Request.Header.Get("traceparent"))

			rec.Observe(maping.Record{
				Method:        ctx.Request.Method,
				RouteTemplate: route,
				Status:        status,
				Duration:      time.Since(start),
				ReqBytes:      adapterutil.ClampNonNegative(ctx.Request.ContentLength),
				// Beego's *context.Response exposes no written-byte count, so RespBytes
				// is reported as 0 (an honest gap, not a fabricated value).
				RespBytes:          0,
				TraceID:            traceID,
				SpanID:             spanID,
				RequestID:          ctx.Request.Header.Get("X-Request-Id"),
				NoStatusReason:     reason,
				DownstreamDuration: maping.DownstreamElapsed(reqCtx),
			})
		}
	}
}
