// Package nethttp is the mAPI-ng adapter for the standard library net/http
// server. Its only job is to extract, after each request completes, the two
// things the Core needs — the registered route template (never the raw path)
// and the final status code — and call the Recorder's Observe. This module pulls
// in nothing but the stdlib plus the core client, so a net/http user takes on no
// framework dependency.
//
// Registration order matters: net/http has no built-in panic recovery, so wrap
// this middleware OUTSIDE (below, in call order) any recover middleware the host
// installs. That way a panic turned into a 500 by the host's recover is simply
// observed here as a 500. The middleware deliberately does not recover panics
// itself — it observes host behavior, it never alters it.
//
// Route templates come from http.Request.Pattern (Go 1.22+), the matched
// ServeMux pattern. A request that matched no pattern reports an empty template
// and is skipped, so the raw path is never observed (which would explode series
// cardinality). A non-ServeMux router that does not set Pattern is skipped the
// same way.
package nethttp

import (
	"net/http"
	"strings"
	"time"

	maping "github.com/arhuman/maping/client"
	"github.com/arhuman/maping/client/adapterutil"
)

// Middleware creates a Recorder from the given options and returns net/http
// middleware that observes each request. With no ingest key resolved the
// Recorder is a no-op, so this is always safe to add (zero-config). The returned
// middleware does not expose the Recorder, so it cannot be shut down; use
// MiddlewareWithRecorder when the host needs to manage the lifecycle.
func Middleware(opts ...maping.Option) func(http.Handler) http.Handler {
	return MiddlewareWithRecorder(maping.NewRecorder(opts...))
}

// MiddlewareWithRecorder returns net/http middleware bound to a caller-owned
// Recorder, so the host controls its lifecycle and can call Shutdown (after
// http.Server.Shutdown).
//
// No ErrorClass is set: stdlib net/http has no error-attach mechanism (unlike
// Gin's c.Error), so the adapter honestly leaves it empty.
func MiddlewareWithRecorder(rec *maping.Recorder) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Install the downstream-time accumulator so a maping.RoundTripper on the
			// host's outbound http.Client can attribute round-trip time to this
			// request. It is inert unless the host wires the RoundTripper and
			// propagates this context, so it costs a single context value otherwise.
			ctx := maping.WithDownstreamTracking(r.Context())
			r = r.WithContext(ctx)

			cw := adapterutil.WrapResponseWriter(w)

			start := time.Now()
			next.ServeHTTP(cw, r)

			route := routeTemplate(r.Pattern)
			if route == "" {
				// Unmatched route (or a router that does not report a pattern): skip to
				// avoid emitting a raw path, which would explode series cardinality.
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

// routeTemplate extracts the path template from an http.Request.Pattern. Go
// 1.22+ formats a matched ServeMux pattern as "[METHOD ][HOST]/PATH" (e.g.
// "GET /users/{id}" or "example.com/static/"). Neither the method nor the host
// contains a "/", so slicing from the first "/" yields the path template alone —
// keeping RouteTemplate path-only and consistent with gin's FullPath() (Method
// is already a separate Record field). An empty pattern, or one with no "/"
// (unmatched, or a non-ServeMux router), returns "".
func routeTemplate(pattern string) string {
	i := strings.IndexByte(pattern, '/')
	if i < 0 {
		return ""
	}
	return pattern[i:]
}
