// Package gin is the mAPI-ng adapter for the Gin web framework. Its only job is
// to extract, after each request completes, the two things the Core needs — the
// registered route template (never the raw path) and the final status code —
// and call the Recorder's Observe. Only this package imports Gin, so a non-Gin
// user never pulls Gin into their binary (docs/context.md → Adapter).
//
// Registration order matters: register this middleware ABOVE gin.Recovery() so
// that when Recovery converts a panic into a 500, this middleware simply
// observes that 500. The middleware deliberately does NOT recover panics itself
// — it observes host behavior, it never alters it.
package gin

import (
	"time"

	"github.com/gin-gonic/gin"

	maping "github.com/arhuman/maping/client"
	"github.com/arhuman/maping/client/adapterutil"
)

// Middleware creates a Recorder from the given options and returns a Gin
// handler that observes each request. With no ingest key resolved the Recorder
// is a no-op, so this is always safe to add (zero-config). The returned handler
// does not expose the Recorder, so it cannot be shut down; use
// MiddlewareWithRecorder when the host needs to manage the lifecycle.
func Middleware(opts ...maping.Option) gin.HandlerFunc {
	return MiddlewareWithRecorder(maping.NewRecorder(opts...))
}

// MiddlewareWithRecorder returns a Gin handler bound to a caller-owned
// Recorder, so the host controls its lifecycle and can call Shutdown (after
// http.Server.Shutdown).
func MiddlewareWithRecorder(rec *maping.Recorder) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Install the downstream-time accumulator so a maping.RoundTripper on the
		// host's outbound http.Client can attribute round-trip time to this request.
		// It is inert unless the host actually wires the RoundTripper and propagates
		// this context, so it costs a single context value otherwise.
		c.Request = c.Request.WithContext(maping.WithDownstreamTracking(c.Request.Context()))

		start := time.Now()
		c.Next()

		route := c.FullPath()
		if route == "" {
			// Unmatched route: no registered template, skip to avoid emitting a
			// raw path (which would explode series cardinality).
			return
		}

		traceID, spanID := adapterutil.ParseTraceparent(c.GetHeader("traceparent"))

		status, reason := adapterutil.ReclassifyNoStatus(c.Request.Context(), c.Writer.Status())

		rec.Observe(maping.Record{
			Method:             c.Request.Method,
			RouteTemplate:      route,
			Status:             status,
			Duration:           time.Since(start),
			ReqBytes:           adapterutil.ClampNonNegative(c.Request.ContentLength),
			RespBytes:          adapterutil.ClampNonNegative(int64(c.Writer.Size())),
			TraceID:            traceID,
			SpanID:             spanID,
			RequestID:          c.GetHeader("X-Request-Id"),
			ErrorClass:         lastErrorLabel(c),
			NoStatusReason:     reason,
			DownstreamDuration: maping.DownstreamElapsed(c.Request.Context()),
		})
	}
}

// lastErrorLabel returns the most recent error attached to the request via
// c.Error(...), or "" when the handler attached none. The Core normalizes and
// bounds it, so the adapter passes the raw message through unshaped.
func lastErrorLabel(c *gin.Context) string {
	if len(c.Errors) == 0 {
		return ""
	}
	return c.Errors.Last().Error()
}
