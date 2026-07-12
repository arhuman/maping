package mapingcompress

import (
	"bytes"
	"io"
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

// TestOptionsNonNil is a smoke test that the exported option constructors return
// usable Connect options.
func TestOptionsNonNil(t *testing.T) {
	require.NotNil(t, HandlerOption())
	require.Len(t, ClientOptions(), 2)
}
