// Package echo is the mAPI-ng adapter for the Echo v4 web framework. Its only
// job is to extract, after each request completes, the two things the Core needs
// — the registered route template (never the raw path) and the final status code
// — and call the Recorder's Observe. Only this package imports Echo, so a non-Echo
// user never pulls Echo into their binary (docs/context.md → Adapter).
//
// Registration order matters: register this middleware BEFORE echo's
// middleware.Recover() (Echo runs middleware in registration order, outer-first)
// so that when Recover converts a panic into a 500, this middleware simply
// observes that 500. The middleware deliberately does NOT recover panics itself
// — it observes host behavior, it never alters it.
//
// Echo reports the matched route template through echo.Context.Path() (e.g.
// "/users/:id"). A request that matched no route reports an empty template and is
// skipped, so the raw path is never observed (which would explode series
// cardinality); the handler's returned error is still propagated so Echo's own
// error handling runs.
package echo

import (
	"errors"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	maping "github.com/arhuman/maping/client"
	"github.com/arhuman/maping/client/adapterutil"
)

// Middleware creates a Recorder from the given options and returns an Echo
// middleware that observes each request. With no ingest key resolved the Recorder
// is a no-op, so this is always safe to add (zero-config). The returned middleware
// does not expose the Recorder, so it cannot be shut down; use
// MiddlewareWithRecorder when the host needs to manage the lifecycle.
func Middleware(opts ...maping.Option) echo.MiddlewareFunc {
	return MiddlewareWithRecorder(maping.NewRecorder(opts...))
}

// MiddlewareWithRecorder returns an Echo middleware bound to a caller-owned
// Recorder, so the host controls its lifecycle and can call Shutdown (after
// echo.Echo.Shutdown).
func MiddlewareWithRecorder(rec *maping.Recorder) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Install the downstream-time accumulator so a maping.RoundTripper on the
			// host's outbound http.Client can attribute round-trip time to this
			// request. It is inert unless the host wires the RoundTripper and
			// propagates this context, so it costs a single context value otherwise.
			// Bind the context once (not inline in SetRequest) so the contextcheck
			// linter sees a single derived context, mirroring the nethttp adapter.
			ctx := maping.WithDownstreamTracking(c.Request().Context())
			c.SetRequest(c.Request().WithContext(ctx))

			start := time.Now()
			err := next(c)

			route := c.Path()
			if route == "" {
				// Unmatched route: no registered template. Skip observing to avoid
				// emitting a raw path, but still propagate the error so Echo's handler
				// (e.g. its 404 handling) runs.
				return err
			}

			res := c.Response()
			status := res.Status
			var errorClass string
			if err != nil {
				errorClass = errorLabel(err)
				if !res.Committed {
					// Echo's centralized HTTPErrorHandler writes the status AFTER this
					// middleware chain unwinds, so res.Status is not yet final for an
					// uncommitted error response. Infer what echo's DEFAULT handler will
					// write, non-invasively (we never call c.Error) — the same kind of
					// assumption gin's adapter makes about gin.Recovery writing 500.
					status = statusFromError(err)
				}
			}

			status, reason := adapterutil.ReclassifyNoStatus(ctx, status)
			traceID, spanID := adapterutil.ParseTraceparent(c.Request().Header.Get("traceparent"))

			rec.Observe(maping.Record{
				Method:             c.Request().Method,
				RouteTemplate:      route,
				Status:             status,
				Duration:           time.Since(start),
				ReqBytes:           adapterutil.ClampNonNegative(c.Request().ContentLength),
				RespBytes:          adapterutil.ClampNonNegative(res.Size),
				TraceID:            traceID,
				SpanID:             spanID,
				RequestID:          c.Request().Header.Get("X-Request-Id"),
				ErrorClass:         errorClass,
				NoStatusReason:     reason,
				DownstreamDuration: maping.DownstreamElapsed(ctx),
			})

			// Always propagate: reading the response is idempotent, and Echo's error
			// handler must still run on a non-nil error.
			return err
		}
	}
}

// statusFromError infers the status echo's DEFAULT HTTPErrorHandler will write
// for a handler-returned error: an *echo.HTTPError carries its own Code, anything
// else maps to 500. This mirrors the default handler's mapping; a host that
// installs a custom HTTPErrorHandler with a different mapping would diverge here.
func statusFromError(err error) int {
	var he *echo.HTTPError
	if errors.As(err, &he) {
		return he.Code
	}
	return http.StatusInternalServerError
}

// errorLabel extracts a raw label from a handler-returned error. For an
// *echo.HTTPError it prefers the wrapped Internal error, then a string message;
// otherwise the error's own message. The Core normalizes and bounds it, so the
// adapter passes the raw message through unshaped (like gin's lastErrorLabel).
func errorLabel(err error) string {
	var he *echo.HTTPError
	if errors.As(err, &he) {
		if he.Internal != nil {
			return he.Internal.Error()
		}
		if msg, ok := he.Message.(string); ok {
			return msg
		}
	}
	return err.Error()
}
