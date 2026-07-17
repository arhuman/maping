package adapterutil

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWrapResponseWriterDefaultStatusOK(t *testing.T) {
	w := WrapResponseWriter(httptest.NewRecorder())

	n, err := w.Write([]byte("hello"))
	require.NoError(t, err)
	assert.Equal(t, 5, n)

	assert.Equal(t, http.StatusOK, w.Status(), "a body write without WriteHeader is an implicit 200")
	assert.Equal(t, int64(5), w.BytesWritten())
}

func TestWrapResponseWriterCapturesStatusAndBytes(t *testing.T) {
	w := WrapResponseWriter(httptest.NewRecorder())

	w.WriteHeader(http.StatusNotFound)
	n, err := w.Write([]byte("not found"))
	require.NoError(t, err)

	assert.Equal(t, http.StatusNotFound, w.Status())
	assert.Equal(t, int64(n), w.BytesWritten())
}

func TestWrapResponseWriterFirstWriteHeaderWins(t *testing.T) {
	w := WrapResponseWriter(httptest.NewRecorder())

	w.WriteHeader(http.StatusTeapot)
	w.WriteHeader(http.StatusInternalServerError)

	assert.Equal(t, http.StatusTeapot, w.Status(), "only the first WriteHeader wins")
}

func TestWrapResponseWriterUnwrap(t *testing.T) {
	inner := httptest.NewRecorder()
	w := WrapResponseWriter(inner)

	assert.Same(t, inner, w.Unwrap())
}

// capableWriter implements http.Flusher, http.Hijacker, and io.ReaderFrom so a
// test can assert the wrapper both satisfies those interfaces and delegates to
// the underlying writer.
type capableWriter struct {
	http.ResponseWriter
	flushed  bool
	hijacked bool
	readFrom bool
	body     strings.Builder
}

func (c *capableWriter) Flush() { c.flushed = true }

func (c *capableWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c.hijacked = true
	return nil, nil, nil
}

func (c *capableWriter) ReadFrom(src io.Reader) (int64, error) {
	c.readFrom = true
	n, err := io.Copy(&c.body, src)
	return n, err
}

func TestWrapResponseWriterPreservesCapabilities(t *testing.T) {
	under := &capableWriter{ResponseWriter: httptest.NewRecorder()}
	w := WrapResponseWriter(under)

	// The wrapper still satisfies the optional interfaces.
	_, isFlusher := any(w).(http.Flusher)
	_, isHijacker := any(w).(http.Hijacker)
	_, isReaderFrom := any(w).(io.ReaderFrom)
	require.True(t, isFlusher)
	require.True(t, isHijacker)
	require.True(t, isReaderFrom)

	w.Flush()
	assert.True(t, under.flushed, "Flush must delegate to the underlying Flusher")

	_, _, err := w.Hijack()
	require.NoError(t, err)
	assert.True(t, under.hijacked, "Hijack must delegate to the underlying Hijacker")

	n, err := w.ReadFrom(strings.NewReader("payload"))
	require.NoError(t, err)
	assert.True(t, under.readFrom, "ReadFrom must delegate to the underlying ReaderFrom")
	assert.Equal(t, int64(7), n)
	assert.Equal(t, int64(7), w.BytesWritten(), "delegated ReadFrom bytes must be counted")
	assert.Equal(t, "payload", under.body.String())
}

// plainWriter implements only http.ResponseWriter, so the wrapper must supply
// the fallbacks: Hijack → ErrNotSupported, Flush → no-op, ReadFrom → io.Copy.
type plainWriter struct {
	body strings.Builder
}

func (p *plainWriter) Header() http.Header         { return http.Header{} }
func (p *plainWriter) Write(b []byte) (int, error) { return p.body.Write(b) }
func (p *plainWriter) WriteHeader(int)             {}

func TestWrapResponseWriterFallbacksWhenUnderlyingSupportsNothing(t *testing.T) {
	plain := &plainWriter{}
	w := WrapResponseWriter(plain)

	// Hijack falls back to ErrNotSupported.
	_, _, err := w.Hijack()
	assert.ErrorIs(t, err, http.ErrNotSupported)

	// Flush no-ops (must not panic).
	assert.NotPanics(t, w.Flush)

	// ReadFrom falls back to io.Copy and still counts bytes.
	n, err := w.ReadFrom(strings.NewReader("payload"))
	require.NoError(t, err)
	assert.Equal(t, int64(7), n)
	assert.Equal(t, int64(7), w.BytesWritten(), "fallback ReadFrom bytes must be counted")
	assert.Equal(t, "payload", plain.body.String())
}
