package nethttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	maping "github.com/arhuman/maping/client"
	mapingv1 "github.com/arhuman/maping/proto/maping/v1"
)

func TestMiddlewareObservesMatchedRoute(t *testing.T) {
	fake := &fakeTransport{}
	rec := maping.NewRecorderForTest(fake)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /users/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("ok"))
	})
	h := MiddlewareWithRecorder(rec)(mux)

	req := httptest.NewRequest(http.MethodGet, "/users/42", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	require.NoError(t, rec.Shutdown(context.Background()))

	sums := fake.summaries()
	require.Len(t, sums, 1)
	s := sums[0]
	assert.Equal(t, http.MethodGet, s.Method)
	assert.Equal(t, "/users/{id}", s.RouteTemplate, "must use the route template with the method prefix stripped")
	assert.Equal(t, mapingv1.StatusClass_STATUS_CLASS_2XX, s.StatusClass)
	assert.Equal(t, uint64(1), s.Count)
	assert.Equal(t, uint64(1), s.StatusCodeBreakdown[http.StatusCreated])
	assert.Positive(t, s.RespBytes)
}

func TestMiddlewareSkipsUnmatchedRoute(t *testing.T) {
	fake := &fakeTransport{}
	rec := maping.NewRecorderForTest(fake)

	// An empty mux matches nothing, so r.Pattern stays empty → skip observe.
	mux := http.NewServeMux()
	h := MiddlewareWithRecorder(rec)(mux)

	req := httptest.NewRequest(http.MethodGet, "/nope", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	require.NoError(t, rec.Shutdown(context.Background()))
	assert.Empty(t, fake.summaries(), "unmatched route must not be observed (raw path never recorded)")
}

// TestMiddlewarePopulatesExemplarIDs drives a real request carrying a
// traceparent and an X-Request-Id and asserts both land on the emitted exemplar.
func TestMiddlewarePopulatesExemplarIDs(t *testing.T) {
	fake := &fakeTransport{}
	rec := maping.NewRecorderForTest(fake)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /x", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	h := MiddlewareWithRecorder(rec)(mux)

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	// Case-insensitive header lookup: set a lower-cased name.
	req.Header.Set("x-request-id", "req-abc")
	h.ServeHTTP(httptest.NewRecorder(), req)

	require.NoError(t, rec.Shutdown(context.Background()))

	sums := fake.summaries()
	require.Len(t, sums, 1)
	exs := sums[0].GetExemplars()
	require.Len(t, exs, 1)
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", exs[0].GetTraceId())
	assert.Equal(t, "00f067aa0ba902b7", exs[0].GetSpanId())
	assert.Equal(t, "req-abc", exs[0].GetRequestId())
}

// TestMiddlewareReclassifiesCanceledRequest confirms a request whose context is
// canceled before the handler writes is reported as NO_STATUS with the
// context-canceled reason.
func TestMiddlewareReclassifiesCanceledRequest(t *testing.T) {
	fake := &fakeTransport{}
	rec := maping.NewRecorderForTest(fake)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /slow", func(_ http.ResponseWriter, _ *http.Request) {
		// Handler writes nothing; the canceled context is what drives NO_STATUS.
	})
	h := MiddlewareWithRecorder(rec)(mux)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/slow", nil).WithContext(ctx)
	h.ServeHTTP(httptest.NewRecorder(), req)

	require.NoError(t, rec.Shutdown(context.Background()))

	sums := fake.summaries()
	require.Len(t, sums, 1)
	s := sums[0]
	assert.Equal(t, mapingv1.StatusClass_STATUS_CLASS_NO_STATUS, s.StatusClass)
	assert.Equal(t, uint64(1), s.GetNoStatusReasons()[uint32(maping.NoStatusContextCanceled)])
}

// TestMiddlewareReturnsHandler verifies that Middleware returns a usable,
// non-nil decorator that is a safe no-op with no ingest key resolved.
func TestMiddlewareReturnsHandler(t *testing.T) {
	mw := Middleware()
	require.NotNil(t, mw, "Middleware must return a non-nil decorator")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("pong"))
	})
	h := mw(mux)

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	w := httptest.NewRecorder()
	assert.NotPanics(t, func() { h.ServeHTTP(w, req) })
	assert.Equal(t, http.StatusOK, w.Code)
}

// fakeTransport captures uploaded summaries so a test can assert what the
// middleware observed, without a live collector.
type fakeTransport struct {
	mu   sync.Mutex
	sums []*mapingv1.Summary
}

func (f *fakeTransport) Register(context.Context, *mapingv1.Handshake) error { return nil }

func (f *fakeTransport) Upload(_ context.Context, req *mapingv1.UploadRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sums = append(f.sums, req.Summaries...)
	return nil
}

func (f *fakeTransport) summaries() []*mapingv1.Summary {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sums
}
