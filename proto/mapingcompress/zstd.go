// Package mapingcompress provides the zstd Connect codec that is part of the
// mAPI-ng wire contract (ADR-0002). Both the client transport and the server
// ingest handler register it so zstd-compressed request bodies negotiate
// successfully — a client that sends zstd needs a server that can decode it.
package mapingcompress

import (
	"io"

	"connectrpc.com/connect"
	"github.com/klauspost/compress/zstd"
)

// Name is the Connect compression identifier for zstd.
const Name = "zstd"

// HandlerOption registers the zstd codec on a Connect handler so the server can
// decode zstd-compressed request bodies.
func HandlerOption() connect.HandlerOption {
	return connect.WithCompression(Name, newDecompressor, newCompressor)
}

// ClientOptions register the zstd codec on a Connect client and select it for
// request bodies. WithAcceptCompression makes zstd available; WithSendCompression
// then chooses it (it requires the prior registration).
func ClientOptions() []connect.ClientOption {
	return []connect.ClientOption{
		connect.WithAcceptCompression(Name, newDecompressor, newCompressor),
		connect.WithSendCompression(Name),
	}
}

// compressor adapts *zstd.Encoder to connect.Compressor. Connect calls Reset to
// rebind the sink and Close to flush before returning it to the pool.
type compressor struct{ enc *zstd.Encoder }

func newCompressor() connect.Compressor {
	// The only errors are for invalid options, and none are passed.
	enc, _ := zstd.NewWriter(nil)
	return &compressor{enc: enc}
}

func (c *compressor) Write(p []byte) (int, error) { return c.enc.Write(p) }
func (c *compressor) Close() error                { return c.enc.Close() }
func (c *compressor) Reset(w io.Writer)           { c.enc.Reset(w) }

// decompressor adapts *zstd.Decoder to connect.Decompressor.
type decompressor struct{ dec *zstd.Decoder }

func newDecompressor() connect.Decompressor {
	dec, _ := zstd.NewReader(nil)
	return &decompressor{dec: dec}
}

func (d *decompressor) Read(p []byte) (int, error) { return d.dec.Read(p) }
func (d *decompressor) Reset(r io.Reader) error    { return d.dec.Reset(r) }

// Close releases the decoder's goroutines. zstd.Decoder.Close is safe to call
// here even after a Reset(nil).
func (d *decompressor) Close() error {
	d.dec.Close()
	return nil
}
