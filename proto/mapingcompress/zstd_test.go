package mapingcompress

import (
	"bytes"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestZstdRoundTrip exercises the compressor/decompressor pair the way Connect
// does: Reset to bind the stream, Write/Close to compress, Reset+Read to inflate.
func TestZstdRoundTrip(t *testing.T) {
	payload := bytes.Repeat([]byte("mAPI-ng summary bytes "), 512)

	var compressed bytes.Buffer
	comp := newCompressor()
	comp.Reset(&compressed)
	_, err := comp.Write(payload)
	require.NoError(t, err)
	require.NoError(t, comp.Close())

	assert.Less(t, compressed.Len(), len(payload), "zstd must shrink a repetitive payload")

	decomp := newDecompressor()
	require.NoError(t, decomp.Reset(&compressed))
	got, err := io.ReadAll(decomp)
	require.NoError(t, err)
	require.NoError(t, decomp.Close())

	assert.Equal(t, payload, got, "decompressed output must match the original")
}

// TestDecompressorReusableAfterClose guards the pool-reuse contract that Connect
// relies on. Connect keeps decompressors in a sync.Pool and reuses each instance
// across requests: putDecompressor calls Close() then Reset(http.NoBody), and a
// later getDecompressor calls Reset(body) on the SAME instance. A terminal
// zstd.Decoder.Close() poisons the pooled decoder so the second decode fails with
// "get decompressor: decoder used after Close" — which silently drops uploads
// (only the highest-frequency route trickles through). This test drives the exact
// pool lifecycle and asserts a reused decompressor still inflates correctly.
func TestDecompressorReusableAfterClose(t *testing.T) {
	compress := func(s string) *bytes.Buffer {
		var buf bytes.Buffer
		comp := newCompressor()
		comp.Reset(&buf)
		_, err := comp.Write([]byte(s))
		require.NoError(t, err)
		require.NoError(t, comp.Close())
		return &buf
	}

	decomp := newDecompressor()

	// Request 1: get (Reset+read) then put (Close + Reset(http.NoBody)).
	first := compress("first pooled upload payload")
	require.NoError(t, decomp.Reset(first))
	_, err := io.ReadAll(decomp)
	require.NoError(t, err)
	require.NoError(t, decomp.Close())
	require.NoError(t, decomp.Reset(http.NoBody)) // Connect's putDecompressor step

	// Request 2: the SAME instance is drawn from the pool and reused. On a
	// terminal Close() this Reset returns ErrDecoderClosed ("decoder used after
	// Close"); it must succeed and decode correctly instead.
	second := compress("second pooled upload payload")
	require.NoError(t, decomp.Reset(second), "reused decompressor must not be poisoned by Close()")
	got, err := io.ReadAll(decomp)
	require.NoError(t, err)
	assert.Equal(t, "second pooled upload payload", string(got))
}

// TestOptionsNonNil is a smoke test that the exported option constructors return
// usable Connect options.
func TestOptionsNonNil(t *testing.T) {
	require.NotNil(t, HandlerOption())
	require.Len(t, ClientOptions(), 2)
}
