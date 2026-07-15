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
	"context"
	"errors"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	maping "github.com/arhuman/maping/client"
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

		traceID, spanID := parseTraceparent(c.GetHeader("traceparent"))

		// Reclassify as NO_STATUS only on a real abort signal — the request context
		// canceled (client disconnect) or its deadline fired — before the response
		// finished. A live context is left as the written status, so an ordinary
		// empty-body handler is never misreported as NO_STATUS.
		status := c.Writer.Status()
		var reason maping.NoStatusReason
		if cause := context.Cause(c.Request.Context()); cause != nil {
			status = 0
			reason = noStatusReasonOf(cause)
		}

		rec.Observe(maping.Record{
			Method:             c.Request.Method,
			RouteTemplate:      route,
			Status:             status,
			Duration:           time.Since(start),
			ReqBytes:           clampNonNegative(c.Request.ContentLength),
			RespBytes:          clampNonNegative(int64(c.Writer.Size())),
			TraceID:            traceID,
			SpanID:             spanID,
			RequestID:          c.GetHeader("X-Request-Id"),
			ErrorClass:         lastErrorLabel(c),
			NoStatusReason:     reason,
			DownstreamDuration: maping.DownstreamElapsed(c.Request.Context()),
		})
	}
}

// noStatusReasonOf maps the request context's cancellation cause to a
// NoStatusReason: a fired deadline vs. a plain cancellation (client disconnect),
// with anything else recorded as OTHER. Panics are converted to a 500 by
// gin.Recovery (registered below this middleware), so they surface as a status,
// not here.
func noStatusReasonOf(cause error) maping.NoStatusReason {
	switch {
	case errors.Is(cause, context.DeadlineExceeded):
		return maping.NoStatusContextDeadline
	case errors.Is(cause, context.Canceled):
		return maping.NoStatusContextCanceled
	default:
		return maping.NoStatusOther
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

// parseTraceparent extracts the trace id and span id from a W3C traceparent
// header (RFC: "version-traceid-spanid-flags", e.g.
// "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"). It is deliberately
// hand-rolled so the client stays free of any OpenTelemetry dependency. Both ids
// are returned empty unless the header is well-formed: exactly four dash-
// separated parts, a 32-hex trace id and a 16-hex span id, neither all-zero
// (the spec's "invalid" sentinel). Best-effort: any deviation yields empties.
func parseTraceparent(h string) (traceID, spanID string) {
	if len(h) == 0 {
		return "", ""
	}
	parts := strings.Split(h, "-")
	if len(parts) != 4 {
		return "", ""
	}
	tid, sid := parts[1], parts[2]
	if !isHex(tid, 32) || allZero(tid) {
		return "", ""
	}
	if !isHex(sid, 16) || allZero(sid) {
		return "", ""
	}
	return tid, sid
}

// isHex reports whether s is exactly n lowercase-or-uppercase hex digits.
func isHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// allZero reports whether s is all ASCII '0's — the W3C "invalid" id sentinel.
func allZero(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '0' {
			return false
		}
	}
	return true
}

// clampNonNegative folds a negative byte count (unknown ContentLength is -1,
// an unwritten body Size is -1) to 0.
func clampNonNegative(n int64) int64 {
	if n < 0 {
		return 0
	}
	return n
}
