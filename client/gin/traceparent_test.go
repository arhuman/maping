package gin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	maping "github.com/arhuman/maping/client"
)

// TestMiddlewarePopulatesExemplarIDs drives a real request carrying a traceparent
// and an X-Request-Id and asserts both land on the emitted exemplar.
func TestMiddlewarePopulatesExemplarIDs(t *testing.T) {
	gin.SetMode(gin.TestMode)

	fake := &fakeTransport{}
	rec := maping.NewRecorderForTest(fake)

	engine := gin.New()
	engine.Use(MiddlewareWithRecorder(rec))
	engine.GET("/x", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	// Case-insensitive header lookup: set a lower-cased name.
	req.Header.Set("x-request-id", "req-abc")
	engine.ServeHTTP(httptest.NewRecorder(), req)

	require.NoError(t, rec.Shutdown(context.Background()))

	sums := fake.summaries()
	require.Len(t, sums, 1)
	exs := sums[0].GetExemplars()
	require.Len(t, exs, 1)
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", exs[0].GetTraceId())
	assert.Equal(t, "00f067aa0ba902b7", exs[0].GetSpanId())
	assert.Equal(t, "req-abc", exs[0].GetRequestId())
}

// TestMiddlewareNoTraceHeaders confirms best-effort: absent headers yield empty
// exemplar ids, not errors.
func TestMiddlewareNoTraceHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)

	fake := &fakeTransport{}
	rec := maping.NewRecorderForTest(fake)

	engine := gin.New()
	engine.Use(MiddlewareWithRecorder(rec))
	engine.GET("/x", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	require.NoError(t, rec.Shutdown(context.Background()))

	sums := fake.summaries()
	require.Len(t, sums, 1)
	exs := sums[0].GetExemplars()
	require.Len(t, exs, 1)
	assert.Empty(t, exs[0].GetTraceId())
	assert.Empty(t, exs[0].GetSpanId())
	assert.Empty(t, exs[0].GetRequestId())
}
