package gin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	maping "github.com/arhuman/maping/client"
	mapingv1 "github.com/arhuman/maping/proto/maping/v1"
)

func TestMiddlewareObservesMatchedRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)

	fake := &fakeTransport{}
	rec := maping.NewRecorderForTest(fake)

	engine := gin.New()
	engine.Use(MiddlewareWithRecorder(rec))
	engine.GET("/users/:id", func(c *gin.Context) {
		c.String(http.StatusCreated, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/users/42", nil)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	// Shutdown drives a final flush; the fake captures the resulting upload.
	require.NoError(t, rec.Shutdown(context.Background()))

	sums := fake.summaries()
	require.Len(t, sums, 1)
	s := sums[0]
	assert.Equal(t, http.MethodGet, s.Method)
	assert.Equal(t, "/users/:id", s.RouteTemplate, "must use the route template, not the raw path")
	assert.Equal(t, mapingv1.StatusClass_STATUS_CLASS_2XX, s.StatusClass)
	assert.Equal(t, uint64(1), s.Count)
	assert.Equal(t, uint64(1), s.StatusCodeBreakdown[http.StatusCreated])
	assert.Positive(t, s.RespBytes)
}

func TestMiddlewareSkipsUnmatchedRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)

	fake := &fakeTransport{}
	rec := maping.NewRecorderForTest(fake)

	engine := gin.New()
	engine.Use(MiddlewareWithRecorder(rec))
	// No route registered → c.FullPath() is empty → skip observe.

	req := httptest.NewRequest(http.MethodGet, "/nope", nil)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	require.NoError(t, rec.Shutdown(context.Background()))
	assert.Empty(t, fake.summaries(), "unmatched route must not be observed")
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

// TestMiddlewareReturnsHandlerFunc verifies that Middleware returns a non-nil
// gin.HandlerFunc and that a request run through an engine using it does not
// panic. With no ingest key resolved the recorder is a no-op (zero-config
// safe), so this test never needs a live collector.
func TestMiddlewareReturnsHandlerFunc(t *testing.T) {
	engine := gin.New()
	// Middleware with no options: no key resolved → no-op recorder.
	mw := Middleware()
	require.NotNil(t, mw, "Middleware must return a non-nil HandlerFunc")

	engine.Use(mw)
	engine.GET("/ping", func(c *gin.Context) {
		c.String(http.StatusOK, "pong")
	})

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	w := httptest.NewRecorder()
	assert.NotPanics(t, func() { engine.ServeHTTP(w, req) })
	assert.Equal(t, http.StatusOK, w.Code)
}

// TestClampNonNegative covers both branches of the clamping helper.
func TestClampNonNegative(t *testing.T) {
	tests := []struct {
		name string
		in   int64
		want int64
	}{
		{"zero", 0, 0},
		{"positive", 42, 42},
		{"negative (unknown length)", -1, 0},
		{"large negative", -100, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, clampNonNegative(tt.in))
		})
	}
}
