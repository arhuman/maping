package adapterutil

import (
	"bufio"
	"io"
	"net"
	"net/http"
)

// ResponseWriter wraps an http.ResponseWriter to capture the two things an
// adapter needs after a stdlib request completes — the final status code and the
// total bytes written — which net/http otherwise discards. Frameworks like Gin
// track these on their own writer; a bare net/http handler does not, so the
// wrapper stands in.
//
// Capabilities (Flusher/Hijacker/ReaderFrom) are preserved by DELEGATION rather
// than by conditional interface assertion, so a library that type-asserts the
// writer keeps working. Unconditional delegation is safe because the only case
// the wrapper "claims" a capability the underlying lacks is a transport that
// could not perform the operation anyway (e.g. hijacking an HTTP/2 conn): the
// delegate then degrades to http.ErrNotSupported (Hijack), a no-op (Flush), or a
// plain io.Copy (ReadFrom) — never a panic or silent data loss. Unwrap exposes
// the real writer for http.ResponseController and any caller that needs more.
type ResponseWriter struct {
	http.ResponseWriter
	status       int
	bytesWritten int64
	wroteHeader  bool
}

// WrapResponseWriter returns a ResponseWriter capturing status and byte count.
// Status defaults to http.StatusOK — net/http's implicit status when a handler
// writes a body without calling WriteHeader.
func WrapResponseWriter(w http.ResponseWriter) *ResponseWriter {
	return &ResponseWriter{ResponseWriter: w, status: http.StatusOK}
}

// Status returns the captured status code (http.StatusOK if none was written).
func (w *ResponseWriter) Status() int { return w.status }

// BytesWritten returns the total bytes forwarded through Write and ReadFrom.
func (w *ResponseWriter) BytesWritten() int64 { return w.bytesWritten }

// WriteHeader records the code on the first call only, then forwards. Later
// calls forward but do not overwrite the captured status, matching net/http's
// "superfluous WriteHeader" behavior.
func (w *ResponseWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status = code
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(code)
}

// Write marks the header written (the default status is already 200), forwards
// the bytes, and counts what the underlying writer accepted.
func (w *ResponseWriter) Write(b []byte) (int, error) {
	w.wroteHeader = true
	n, err := w.ResponseWriter.Write(b)
	w.bytesWritten += int64(n)
	return n, err
}

// Unwrap returns the embedded writer (Go 1.20+ convention) so
// http.ResponseController reaches the real writer.
func (w *ResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Flush forwards to the underlying http.Flusher, or no-ops when the transport
// cannot flush (SSE without buffering support).
func (w *ResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack forwards to the underlying http.Hijacker, or returns
// http.ErrNotSupported when the transport cannot be hijacked (websockets over
// HTTP/2).
func (w *ResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

// ReadFrom delegates to the underlying io.ReaderFrom when present (preserving
// sendfile), otherwise falls back to io.Copy. Either way the copied bytes are
// counted, so RespBytes stays correct with or without the fast path.
func (w *ResponseWriter) ReadFrom(src io.Reader) (int64, error) {
	w.wroteHeader = true
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		n, err := rf.ReadFrom(src)
		w.bytesWritten += n
		return n, err
	}
	n, err := io.Copy(w.ResponseWriter, src)
	w.bytesWritten += n
	return n, err
}
