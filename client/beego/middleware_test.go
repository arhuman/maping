package beego

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/beego/beego/v2/server/web"
	beecontext "github.com/beego/beego/v2/server/web/context"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	maping "github.com/arhuman/maping/client"
	mapingv1 "github.com/arhuman/maping/proto/maping/v1"
)

// newHandler builds an isolated Beego router (never the global app state) with
// the filter chain composed around a single registered route. InsertFilterChain
// only records config; Init() composes it around serveHttp, so it MUST be called
// before ServeHTTP or the filter never runs.
func newHandler(t *testing.T, rec *maping.Recorder, pattern string, h web.HandleFunc) http.Handler {
	t.Helper()
	reg := web.NewControllerRegister()
	reg.InsertFilterChain("/*", FilterWithRecorder(rec))
	reg.Get(pattern, h)
	reg.Init()
	return reg
}

func TestMiddlewareObservesMatchedRoute(t *testing.T) {
	fake := &fakeTransport{}
	rec := maping.NewRecorderForTest(fake)

	h := newHandler(t, rec, "/users/:id", func(ctx *beecontext.Context) {
		ctx.WriteString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/users/42", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	require.NoError(t, rec.Shutdown(context.Background()))

	sums := fake.summaries()
	require.Len(t, sums, 1)
	s := sums[0]
	assert.Equal(t, http.MethodGet, s.Method)
	assert.Equal(t, "/users/:id", s.RouteTemplate, "must use the route template, not the raw path")
	assert.Equal(t, mapingv1.StatusClass_STATUS_CLASS_2XX, s.StatusClass)
	assert.Equal(t, uint64(1), s.Count)
	assert.Equal(t, uint64(1), s.StatusCodeBreakdown[http.StatusOK])
	// Beego's *context.Response exposes no byte count, so RespBytes is always 0.
	assert.Zero(t, s.RespBytes)
}

// sleepRT is a RoundTripper that sleeps a fixed amount and returns a 200, so a
// downstream call made through it records deterministically non-zero time.
type sleepRT struct{ d time.Duration }

func (s sleepRT) RoundTrip(*http.Request) (*http.Response, error) {
	time.Sleep(s.d)
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       http.NoBody,
		Header:     make(http.Header),
	}, nil
}

// TestMiddlewarePropagatesRequestContext proves end-to-end that the
// downstream-tracking context installed on ctx.Request before next(ctx) reaches
// the controller — i.e. Beego threads the same *http.Request through routing to
// the handler. The handler makes an outbound call through a maping RoundTripper
// using ctx.Request.Context(); that call increments the accumulator the filter
// later reads, so a non-zero SumDownstreamDurationNs on the emitted summary can
// only happen if the filter's context reached the handler intact.
func TestMiddlewarePropagatesRequestContext(t *testing.T) {
	fake := &fakeTransport{}
	rec := maping.NewRecorderForTest(fake)

	client := &http.Client{Transport: maping.NewRoundTripper(sleepRT{d: 5 * time.Millisecond})}
	h := newHandler(t, rec, "/probe", func(ctx *beecontext.Context) {
		req, _ := http.NewRequestWithContext(ctx.Request.Context(), http.MethodGet, "http://downstream.local", nil)
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		ctx.WriteString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	require.NoError(t, rec.Shutdown(context.Background()))

	sums := fake.summaries()
	require.Len(t, sums, 1)
	assert.Positive(t, sums[0].GetSumDownstreamDurationNs(),
		"downstream-tracking context must propagate to the controller and back to the filter")
}

// TestMiddlewareSkipsUnmatchedRoute drives an unregistered path and asserts
// nothing is observed, so the raw path is never recorded.
func TestMiddlewareSkipsUnmatchedRoute(t *testing.T) {
	fake := &fakeTransport{}
	rec := maping.NewRecorderForTest(fake)

	// Register one route, then hit a different, unmatched path → Beego 404, no
	// stored RouterPattern → skip.
	h := newHandler(t, rec, "/users/:id", func(ctx *beecontext.Context) {
		ctx.WriteString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/nope", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	require.NoError(t, rec.Shutdown(context.Background()))
	assert.Empty(t, fake.summaries(), "unmatched route must not be observed (raw path never recorded)")
}

// TestMiddlewareObservesHandlerWrittenStatus confirms a handler that sets an
// explicit non-200 status has that status observed.
func TestMiddlewareObservesHandlerWrittenStatus(t *testing.T) {
	fake := &fakeTransport{}
	rec := maping.NewRecorderForTest(fake)

	h := newHandler(t, rec, "/created", func(ctx *beecontext.Context) {
		ctx.ResponseWriter.WriteHeader(http.StatusCreated)
		ctx.WriteString("made")
	})

	req := httptest.NewRequest(http.MethodGet, "/created", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	require.NoError(t, rec.Shutdown(context.Background()))

	sums := fake.summaries()
	require.Len(t, sums, 1)
	s := sums[0]
	assert.Equal(t, mapingv1.StatusClass_STATUS_CLASS_2XX, s.StatusClass)
	assert.Equal(t, uint64(1), s.StatusCodeBreakdown[http.StatusCreated])
}

// TestMiddlewarePopulatesExemplarIDs drives a real request carrying a traceparent
// and an X-Request-Id and asserts both land on the emitted exemplar.
func TestMiddlewarePopulatesExemplarIDs(t *testing.T) {
	fake := &fakeTransport{}
	rec := maping.NewRecorderForTest(fake)

	h := newHandler(t, rec, "/x", func(ctx *beecontext.Context) {
		ctx.WriteString("ok")
	})

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
// canceled before dispatch is reported as NO_STATUS with the context-canceled
// reason. Beego does not abort on a canceled context, so the handler still runs;
// ReclassifyNoStatus is what drives the NO_STATUS on the observed record.
func TestMiddlewareReclassifiesCanceledRequest(t *testing.T) {
	fake := &fakeTransport{}
	rec := maping.NewRecorderForTest(fake)

	h := newHandler(t, rec, "/slow", func(_ *beecontext.Context) {
		// Handler writes nothing; the canceled context is what drives NO_STATUS.
	})

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

// TestFilterReturnsChain verifies that Filter returns a usable, non-nil filter
// chain that is a safe no-op with no ingest key resolved.
func TestFilterReturnsChain(t *testing.T) {
	chain := Filter()
	require.NotNil(t, chain, "Filter must return a non-nil FilterChain")

	reg := web.NewControllerRegister()
	reg.InsertFilterChain("/*", chain)
	reg.Get("/ping", func(ctx *beecontext.Context) { ctx.WriteString("pong") })
	reg.Init()

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	w := httptest.NewRecorder()
	assert.NotPanics(t, func() { reg.ServeHTTP(w, req) })
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
