// Package transport wraps the generated Connect IngestService client with the
// wire policy mAPI-ng requires: the gRPC protocol (ADR-0002) over a dedicated
// HTTP client with an explicit timeout, zstd send-compression, and cleartext
// HTTP/2 (H2C) for local/dev http:// endpoints. It exposes a narrow surface
// (Upload, Register) so the Core recorder depends on an interface, not on
// Connect types.
package transport

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"

	mapingv1 "github.com/arhuman/maping/proto/maping/v1"
	"github.com/arhuman/maping/proto/maping/v1/mapingv1connect"
	"github.com/arhuman/maping/proto/mapingcompress"
)

// httpClientTimeout bounds a single upload/register round-trip. Uploads are
// fire-and-forget, so a stuck collector must never wedge the host indefinitely.
const httpClientTimeout = 10 * time.Second

// Client is the transport-level ingest client. It is safe for concurrent use.
type Client struct {
	svc mapingv1connect.IngestServiceClient
	key string // ingest key, sent as an Authorization: Bearer header.
}

// New builds a Client for the given base endpoint URL and ingest key. An http://
// scheme selects H2C (cleartext HTTP/2) for local/dev; https:// uses TLS HTTP/2.
// Any other scheme is rejected. The key is attached as a bearer
// Authorization header on every request so the collector can resolve the tenant.
func New(endpoint, key string) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("transport: parse endpoint: %w", err)
	}

	var httpClient *http.Client
	switch u.Scheme {
	case "https":
		httpClient = &http.Client{
			Timeout:   httpClientTimeout,
			Transport: &http2.Transport{},
		}
	case "http":
		httpClient = &http.Client{
			Timeout: httpClientTimeout,
			Transport: &http2.Transport{
				AllowHTTP: true,
				DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, network, addr)
				},
			},
		}
	default:
		return nil, fmt.Errorf("transport: unsupported endpoint scheme %q", u.Scheme)
	}

	// gRPC protocol (ADR-0002) plus the shared zstd codec (mapingcompress) so the
	// client and the collector negotiate the same compression — one source of
	// truth for the wire codec.
	opts := append([]connect.ClientOption{connect.WithGRPC()}, mapingcompress.ClientOptions()...)
	svc := mapingv1connect.NewIngestServiceClient(httpClient, endpoint, opts...)

	return &Client{svc: svc, key: key}, nil
}

// authHeader attaches the ingest key as a bearer Authorization header, matching
// what the collector's authenticate step reads.
func (c *Client) authHeader(r interface{ Header() http.Header }) {
	if c.key != "" {
		r.Header().Set("Authorization", "Bearer "+c.key)
	}
}

// Upload sends one batched UploadRequest to the collector.
//
//nolint:dupl // Upload and Register are parallel single-RPC wrappers over different service methods and types; unifying them would obscure, not simplify.
func (c *Client) Upload(ctx context.Context, req *mapingv1.UploadRequest) error {
	r := connect.NewRequest(req)
	c.authHeader(r)
	if _, err := c.svc.Upload(ctx, r); err != nil {
		return fmt.Errorf("transport: upload: %w", err)
	}
	return nil
}

// Register sends the one-time startup Handshake.
//
//nolint:dupl // parallel single-RPC wrapper; see Upload.
func (c *Client) Register(ctx context.Context, hs *mapingv1.Handshake) error {
	r := connect.NewRequest(hs)
	c.authHeader(r)
	if _, err := c.svc.Register(ctx, r); err != nil {
		return fmt.Errorf("transport: register: %w", err)
	}
	return nil
}
