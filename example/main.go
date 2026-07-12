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
package main

import (
	"context"
	"errors"
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
	r.GET("/boom", func(c *gin.Context) {
		panic("boom") // recorded as 5xx, re-raised by gin.Recovery
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
