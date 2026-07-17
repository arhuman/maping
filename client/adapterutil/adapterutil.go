// Package adapterutil holds the framework-agnostic helpers shared by every
// mAPI-ng web adapter (gin today; echo/chi/beego/net-http next). Each adapter's
// job is the same — after a request completes, extract exemplar breadcrumbs, a
// final status, and byte counts — so the parts that don't touch a framework's
// types live here once, single-sourced, rather than being copy-pasted per
// adapter. It imports only the stdlib plus the core client (for NoStatusReason);
// the core never imports this package, so there is no cycle.
package adapterutil

import (
	"context"
	"errors"
	"strings"

	maping "github.com/arhuman/maping/client"
)

// ParseTraceparent extracts the trace id and span id from a W3C traceparent
// header (RFC: "version-traceid-spanid-flags", e.g.
// "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"). It is deliberately
// hand-rolled so the client stays free of any OpenTelemetry dependency. Both ids
// are returned empty unless the header is well-formed: exactly four dash-
// separated parts, a 32-hex trace id and a 16-hex span id, neither all-zero
// (the spec's "invalid" sentinel). Best-effort: any deviation yields empties.
func ParseTraceparent(header string) (traceID, spanID string) {
	if len(header) == 0 {
		return "", ""
	}
	parts := strings.Split(header, "-")
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

// ClampNonNegative folds a negative byte count (unknown ContentLength is -1,
// an unwritten body Size is -1) to 0.
func ClampNonNegative(n int64) int64 {
	if n < 0 {
		return 0
	}
	return n
}

// ReclassifyNoStatus decides whether a completed request should be reported as
// NO_STATUS. It reclassifies only on a real abort signal — the request context
// canceled (client disconnect) or its deadline fired — before the response
// finished: it returns (0, reason), mapping a fired deadline vs. a plain
// cancellation, with anything else recorded as OTHER. A live context (no cause)
// is left as the written status and NoStatusUnspecified, so an ordinary
// empty-body handler is never misreported as NO_STATUS. Panics are converted to
// a 500 by the host's recovery middleware, so they surface as a status, not here.
func ReclassifyNoStatus(ctx context.Context, status int) (int, maping.NoStatusReason) {
	cause := context.Cause(ctx)
	if cause == nil {
		return status, maping.NoStatusUnspecified
	}
	switch {
	case errors.Is(cause, context.DeadlineExceeded):
		return 0, maping.NoStatusContextDeadline
	case errors.Is(cause, context.Canceled):
		return 0, maping.NoStatusContextCanceled
	default:
		return 0, maping.NoStatusOther
	}
}
