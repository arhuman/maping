// Command example is a runnable Gin app instrumented with mAPI-ng. It is the
// 60-second quickstart made real: import the middleware, register it ABOVE
// gin.Recovery(), set MAPING_KEY, and every request is aggregated and shipped.
//
// Run it against a local collector:
//
//	MAPING_KEY=dev-key MAPING_ENDPOINT=http://localhost:8080 go run .
//
// With no MAPING_KEY set, the middleware is a no-op and the app runs untouched —
// the zero-config safety guarantee.
//
// Beyond the basic 2xx/5xx routes, it also exercises the debuggability signals so
// every dashboard panel has something to show: labelled errors (error classes),
// aborted requests (no-status reasons), and an outbound call wrapped in
// maping.NewRoundTripper (self-vs-downstream time). Per-instance USE gauges are
// sampled automatically each flush, no route needed.
package main

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	maping "github.com/arhuman/maping/client"
	mapinggin "github.com/arhuman/maping/client/gin"
)

// downstreamClient wraps the default transport in maping.NewRoundTripper, so the
// time its requests spend waiting is attributed to the calling endpoint (the
// self-vs-downstream split). A real app sets this on the http.Client it uses for
// outbound calls and propagates the inbound request context to them.
var downstreamClient = &http.Client{Transport: maping.NewRoundTripper(nil)}

func main() {
	// One recorder for the process lifetime so we can flush it on shutdown.
	rec := maping.NewRecorder(maping.WithService("example-api"))

	r := gin.New()
	// mAPI-ng sits ABOVE gin.Recovery() so it observes the 500 Recovery writes
	// on a panic, without altering host behavior.
	r.Use(mapinggin.MiddlewareWithRecorder(rec))
	r.Use(gin.Recovery())

	r.GET("/hello/:name", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"hello": c.Param("name")})
	})
	r.GET("/boom", func(_ *gin.Context) {
		panic("boom") // recorded as 5xx, re-raised by gin.Recovery
	})

	// Labelled errors — the handler attaches an error via c.Error, which the
	// adapter reads and the Core normalizes into a bounded error class.
	r.GET("/db-error", func(c *gin.Context) {
		_ = c.Error(errors.New("db pool exhausted")) // -> DB_POOL_EXHAUSTED
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
	})
	r.GET("/upstream-timeout", func(c *gin.Context) {
		_ = c.Error(errors.New("upstream timeout")) // -> UPSTREAM_TIMEOUT
		c.JSON(http.StatusBadGateway, gin.H{"error": "bad gateway"})
	})

	// Aborted requests — the request context ends before a status is written, so
	// the adapter records NO_STATUS with the reason it can derive from the cause.
	r.GET("/timeout", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), time.Millisecond)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		<-ctx.Done() // deadline fires -> NO_STATUS / CONTEXT_DEADLINE
	})
	r.GET("/canceled", func(c *gin.Context) {
		ctx, cancel := context.WithCancel(c.Request.Context())
		c.Request = c.Request.WithContext(ctx)
		cancel() // -> NO_STATUS / CONTEXT_CANCELED
	})

	// Downstream time — an outbound call through the maping RoundTripper. /sleepy
	// is a local stand-in for a slow dependency; its wait shows up as downstream
	// time on /downstream and as this endpoint's own latency on /sleepy.
	r.GET("/downstream", func(c *gin.Context) {
		req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, "http://localhost:9090/sleepy", nil)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "build request"})
			return
		}
		resp, err := downstreamClient.Do(req)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "downstream failed"})
			return
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	r.GET("/sleepy", func(c *gin.Context) {
		time.Sleep(40 * time.Millisecond)
		c.JSON(http.StatusOK, gin.H{"slept": true})
	})

	srv := &http.Server{Addr: ":9090", Handler: r, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Println("example-api listening on :9090")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	// Wait for a termination signal, then shut down in the correct order:
	// stop the HTTP server FIRST (so no new requests), then flush the recorder
	// (Shutdown must run after http.Server.Shutdown).
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("http shutdown: %v", err)
	}
	if err := rec.Shutdown(ctx); err != nil {
		log.Printf("recorder shutdown: %v", err)
	}
}
