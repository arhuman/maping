package transport

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mapingv1 "github.com/arhuman/maping/proto/maping/v1"
	"github.com/arhuman/maping/proto/maping/v1/mapingv1connect"
	"github.com/arhuman/maping/proto/mapingcompress"
)

func TestNewEndpointSchemes(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		wantErr  bool
	}{
		{name: "https", endpoint: "https://ingest.maping.dev", wantErr: false},
		{name: "http H2C", endpoint: "http://localhost:8080", wantErr: false},
		{name: "invalid scheme", endpoint: "ftp://nope", wantErr: true},
		{name: "unparseable", endpoint: "://bad", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := New(tt.endpoint, "test-key")
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, c)
				return
			}
			require.NoError(t, err)
			assert.NotNil(t, c)
		})
	}
}

// fakeIngestService is a minimal IngestServiceHandler that records calls and
// can be told to fail. It is used to stand up a real Connect server in-process
// via httptest.Server so Upload and Register can be driven end-to-end without a
// live collector.
type fakeIngestService struct {
	mapingv1connect.UnimplementedIngestServiceHandler

	// failCode, when non-zero, causes all RPCs to return a Connect error with
	// that code. 0 means "succeed".
	failCode connect.Code

	registers []*mapingv1.Handshake
	uploads   []*mapingv1.UploadRequest
}

func (s *fakeIngestService) Register(
	_ context.Context,
	req *connect.Request[mapingv1.Handshake],
) (*connect.Response[mapingv1.RegisterResponse], error) {
	if s.failCode != 0 {
		return nil, connect.NewError(s.failCode, errors.New("fake register error"))
	}
	s.registers = append(s.registers, req.Msg)
	return connect.NewResponse(&mapingv1.RegisterResponse{}), nil
}

func (s *fakeIngestService) Upload(
	_ context.Context,
	req *connect.Request[mapingv1.UploadRequest],
) (*connect.Response[mapingv1.UploadResponse], error) {
	if s.failCode != 0 {
		return nil, connect.NewError(s.failCode, errors.New("fake upload error"))
	}
	s.uploads = append(s.uploads, req.Msg)
	return connect.NewResponse(&mapingv1.UploadResponse{}), nil
}

// newTestServer starts an httptest.Server serving the fakeIngestService over
// plain HTTP with unencrypted HTTP/2 (H2C). It returns the server and a
// transport Client wired to it. The caller is responsible for closing the
// server (registered as t.Cleanup automatically).
func newTestServer(t *testing.T, svc *fakeIngestService) (*httptest.Server, *Client) {
	t.Helper()

	mux := http.NewServeMux()
	path, handler := mapingv1connect.NewIngestServiceHandler(
		svc,
		mapingcompress.HandlerOption(),
	)
	mux.Handle(path, handler)

	// Use an unstarted server so we can configure H2C (unencrypted HTTP/2)
	// before starting, matching what the transport Client sends.
	srv := httptest.NewUnstartedServer(mux)
	p := new(http.Protocols)
	p.SetHTTP1(true)
	p.SetUnencryptedHTTP2(true)
	srv.Config.Protocols = p
	srv.Start()
	t.Cleanup(srv.Close)

	// The transport's New() H2C branch is selected by the http:// scheme.
	c, err := New(srv.URL, "test-key")
	require.NoError(t, err)
	return srv, c
}

// newTestServerWithHandler lets a test intercept raw requests before they
// reach the Connect handler (used by TestAuthHeaderForwardedOnUpload).
func newTestServerWithHandler(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(h)
	p := new(http.Protocols)
	p.SetHTTP1(true)
	p.SetUnencryptedHTTP2(true)
	srv.Config.Protocols = p
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

// TestAuthHeader verifies that authHeader sets the Authorization header when a
// key is present and is a no-op when the key is empty.
func TestAuthHeader(t *testing.T) {
	tests := []struct {
		name       string
		key        string
		wantHeader string
	}{
		{
			name:       "non-empty key sets bearer header",
			key:        "my-secret",
			wantHeader: "Bearer my-secret",
		},
		{
			name:       "empty key leaves header unset",
			key:        "",
			wantHeader: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Client{key: tt.key}
			// Use a real Connect request as the interface receiver.
			req := connect.NewRequest(&mapingv1.UploadRequest{})
			c.authHeader(req)
			assert.Equal(t, tt.wantHeader, req.Header().Get("Authorization"))
		})
	}
}

// TestUploadSuccess verifies that Upload sends the request to the server and
// returns nil on success.
func TestUploadSuccess(t *testing.T) {
	svc := &fakeIngestService{}
	_, client := newTestServer(t, svc)

	req := &mapingv1.UploadRequest{
		Envelope: &mapingv1.Envelope{Service: "test-svc"},
	}
	err := client.Upload(context.Background(), req)
	require.NoError(t, err)
	require.Len(t, svc.uploads, 1)
	assert.Equal(t, "test-svc", svc.uploads[0].Envelope.Service)
}

// TestUploadServerError verifies that a server-side error surfaces as a
// non-nil error with the "transport: upload:" prefix.
func TestUploadServerError(t *testing.T) {
	svc := &fakeIngestService{failCode: connect.CodeInternal}
	_, client := newTestServer(t, svc)

	err := client.Upload(context.Background(), &mapingv1.UploadRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "transport: upload:")
}

// TestRegisterSuccess verifies that Register sends the handshake and returns
// nil on success.
func TestRegisterSuccess(t *testing.T) {
	svc := &fakeIngestService{}
	_, client := newTestServer(t, svc)

	hs := &mapingv1.Handshake{Service: "test-svc", Instance: "i1", SdkVersion: "0.1.0"}
	err := client.Register(context.Background(), hs)
	require.NoError(t, err)
	require.Len(t, svc.registers, 1)
	assert.Equal(t, "test-svc", svc.registers[0].Service)
}

// TestRegisterServerError verifies that a server-side error surfaces as a
// non-nil error with the "transport: register:" prefix.
func TestRegisterServerError(t *testing.T) {
	svc := &fakeIngestService{failCode: connect.CodeUnavailable}
	_, client := newTestServer(t, svc)

	err := client.Register(context.Background(), &mapingv1.Handshake{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "transport: register:")
}

// TestAuthHeaderForwardedOnUpload asserts that the Authorization header is
// present on the request the server sees.
func TestAuthHeaderForwardedOnUpload(t *testing.T) {
	// Capture the raw Authorization header from the inbound request.
	var gotAuth string
	mux := http.NewServeMux()
	path, handler := mapingv1connect.NewIngestServiceHandler(
		&fakeIngestService{},
		mapingcompress.HandlerOption(),
	)
	// Wrap the handler to peek at the header before forwarding.
	mux.Handle(path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		handler.ServeHTTP(w, r)
	}))

	srv := newTestServerWithHandler(t, mux)

	c, err := New(srv.URL, "secret-key")
	require.NoError(t, err)

	require.NoError(t, c.Upload(context.Background(), &mapingv1.UploadRequest{}))
	assert.Equal(t, "Bearer secret-key", gotAuth, "ingest key must be forwarded as bearer token")
}
