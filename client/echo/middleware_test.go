package echo

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	maping "github.com/arhuman/maping/client"
	mapingv1 "github.com/arhuman/maping/proto/maping/v1"
)

func TestMiddlewareObservesMatchedRoute(t *testing.T) {
	fake := &fakeTransport{}
	rec := maping.NewRecorderForTest(fake)

	e := echo.New()
	e.Use(MiddlewareWithRecorder(rec))
	e.GET("/users/:id", func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/users/42", nil)
	e.ServeHTTP(httptest.NewRecorder(), req)

	require.NoError(t, rec.Shutdown(context.Background()))

	sums := fake.summaries()
	require.Len(t, sums, 1)
	s := sums[0]
	assert.Equal(t, http.MethodGet, s.Method)
	assert.Equal(t, "/users/:id", s.RouteTemplate, "must use the route template, not the raw path")
	assert.Equal(t, mapingv1.StatusClass_STATUS_CLASS_2XX, s.StatusClass)
	assert.Equal(t, uint64(1), s.Count)
	assert.Equal(t, uint64(1), s.StatusCodeBreakdown[http.StatusOK])
	assert.Positive(t, s.RespBytes)
}

// TestMiddlewareSkipsUnmatchedRoute drives an unregistered path and asserts
// nothing is observed. It also records what c.Path() was for the unmatched route.
func TestMiddlewareSkipsUnmatchedRoute(t *testing.T) {
	fake := &fakeTransport{}
	rec := maping.NewRecorderForTest(fake)

	var observedPath string
	var pathSeen bool

	e := echo.New()
	// A probe middleware installed AFTER the observer captures c.Path() for the
	// unmatched request so the test can assert (and document) its value.
	e.Use(MiddlewareWithRecorder(rec))
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			observedPath = c.Path()
			pathSeen = true
			return next(c)
		}
	})
	// No route registered → Echo runs its NotFoundHandler.

	req := httptest.NewRequest(http.MethodGet, "/nope", nil)
	e.ServeHTTP(httptest.NewRecorder(), req)

	require.NoError(t, rec.Shutdown(context.Background()))
	assert.Empty(t, fake.summaries(), "unmatched route must not be observed (raw path never recorded)")
	require.True(t, pathSeen, "probe middleware should have run")
	assert.Empty(t, observedPath, "Echo reports an empty c.Path() for an unmatched route")
}

// TestMiddlewareObservesHTTPErrorStatus confirms an *echo.HTTPError returned from
// a handler (response not pre-committed) is observed with the error's status and
// a non-empty ErrorClass, inferred before Echo's HTTPErrorHandler writes it.
func TestMiddlewareObservesHTTPErrorStatus(t *testing.T) {
	fake := &fakeTransport{}
	rec := maping.NewRecorderForTest(fake)

	e := echo.New()
	e.Use(MiddlewareWithRecorder(rec))
	e.GET("/teapot", func(_ echo.Context) error {
		return echo.NewHTTPError(http.StatusTeapot)
	})

	req := httptest.NewRequest(http.MethodGet, "/teapot", nil)
	e.ServeHTTP(httptest.NewRecorder(), req)

	require.NoError(t, rec.Shutdown(context.Background()))

	sums := fake.summaries()
	require.Len(t, sums, 1)
	s := sums[0]
	assert.Equal(t, mapingv1.StatusClass_STATUS_CLASS_4XX, s.StatusClass)
	assert.Equal(t, uint64(1), s.StatusCodeBreakdown[http.StatusTeapot])
	assert.NotEmpty(t, s.GetErrorClassBreakdown(), "an HTTPError must yield a non-empty ErrorClass")
}

// TestMiddlewareObservesPlainErrorAs500 confirms a plain (non-HTTPError) error is
// inferred as the 500 echo's default handler will write.
func TestMiddlewareObservesPlainErrorAs500(t *testing.T) {
	fake := &fakeTransport{}
	rec := maping.NewRecorderForTest(fake)

	e := echo.New()
	e.Use(MiddlewareWithRecorder(rec))
	e.GET("/boom", func(_ echo.Context) error {
		return errors.New("boom")
	})

	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	e.ServeHTTP(httptest.NewRecorder(), req)

	require.NoError(t, rec.Shutdown(context.Background()))

	sums := fake.summaries()
	require.Len(t, sums, 1)
	s := sums[0]
	assert.Equal(t, mapingv1.StatusClass_STATUS_CLASS_5XX, s.StatusClass)
	assert.Equal(t, uint64(1), s.StatusCodeBreakdown[http.StatusInternalServerError])
}

// TestMiddlewareObservesHandlerWrittenStatus confirms that when the handler writes
// its own status and returns nil, that written status is observed as-is.
func TestMiddlewareObservesHandlerWrittenStatus(t *testing.T) {
	fake := &fakeTransport{}
	rec := maping.NewRecorderForTest(fake)

	e := echo.New()
	e.Use(MiddlewareWithRecorder(rec))
	e.GET("/created", func(c echo.Context) error {
		return c.String(http.StatusCreated, "made")
	})

	req := httptest.NewRequest(http.MethodGet, "/created", nil)
	e.ServeHTTP(httptest.NewRecorder(), req)

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

	e := echo.New()
	e.Use(MiddlewareWithRecorder(rec))
	e.GET("/x", func(c echo.Context) error { return c.String(http.StatusOK, "ok") })

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	// Case-insensitive header lookup: set a lower-cased name.
	req.Header.Set("x-request-id", "req-abc")
	e.ServeHTTP(httptest.NewRecorder(), req)

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
// canceled before the handler responds is reported as NO_STATUS with the
// context-canceled reason.
func TestMiddlewareReclassifiesCanceledRequest(t *testing.T) {
	fake := &fakeTransport{}
	rec := maping.NewRecorderForTest(fake)

	e := echo.New()
	e.Use(MiddlewareWithRecorder(rec))
	e.GET("/slow", func(_ echo.Context) error {
		// Handler writes nothing; the canceled context is what drives NO_STATUS.
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/slow", nil).WithContext(ctx)
	e.ServeHTTP(httptest.NewRecorder(), req)

	require.NoError(t, rec.Shutdown(context.Background()))

	sums := fake.summaries()
	require.Len(t, sums, 1)
	s := sums[0]
	assert.Equal(t, mapingv1.StatusClass_STATUS_CLASS_NO_STATUS, s.StatusClass)
	assert.Equal(t, uint64(1), s.GetNoStatusReasons()[uint32(maping.NoStatusContextCanceled)])
}

// TestMiddlewareReturnsMiddlewareFunc verifies that Middleware returns a usable,
// non-nil middleware that is a safe no-op with no ingest key resolved.
func TestMiddlewareReturnsMiddlewareFunc(t *testing.T) {
	mw := Middleware()
	require.NotNil(t, mw, "Middleware must return a non-nil MiddlewareFunc")

	e := echo.New()
	e.Use(mw)
	e.GET("/ping", func(c echo.Context) error { return c.String(http.StatusOK, "pong") })

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	w := httptest.NewRecorder()
	assert.NotPanics(t, func() { e.ServeHTTP(w, req) })
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
