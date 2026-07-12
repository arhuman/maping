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
		start := time.Now()
		c.Next()

		route := c.FullPath()
		if route == "" {
			// Unmatched route: no registered template, skip to avoid emitting a
			// raw path (which would explode series cardinality).
			return
		}

		rec.Observe(maping.Record{
			Method:        c.Request.Method,
			RouteTemplate: route,
			Status:        c.Writer.Status(),
			Duration:      time.Since(start),
			ReqBytes:      clampNonNegative(c.Request.ContentLength),
			RespBytes:     clampNonNegative(int64(c.Writer.Size())),
		})
	}
}

// clampNonNegative folds a negative byte count (unknown ContentLength is -1,
// an unwritten body Size is -1) to 0.
func clampNonNegative(n int64) int64 {
	if n < 0 {
		return 0
	}
	return n
}
