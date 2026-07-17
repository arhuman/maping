// Command trafficgen continuously uploads instrumented telemetry to the local
// mAPI-ng collector (dev-key -> dev-tenant) so the dashboard has live data.
// Stop it by killing the process. Service name is fixed: "live-generator".
//
// By default it drives a mix of healthy routes. Set GEN_FAULT=<name> to also
// drive one fault from the /fault/* test-bed (package faults) so the dashboard
// shows a real anomaly to diagnose, e.g. GEN_FAULT=sometimesbusy or
// GEN_FAULT=flaky. One fault at a time keeps the process-global signals (CPU
// ramp, goroutine leak) legible. GEN_FAULT_PARAMS appends intensity knobs, e.g.
// GEN_FAULT=flaky GEN_FAULT_PARAMS=pct=40.
package main

import (
	"context"
	"log"
	"math/rand"
	"net/http/httptest"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	maping "github.com/arhuman/maping/client"
	mapinggin "github.com/arhuman/maping/client/gin"
	"github.com/arhuman/maping/example/faults"
)

func main() {
	endpoint := os.Getenv("GEN_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:8086"
	}
	svc := os.Getenv("GEN_SERVICE")
	if svc == "" {
		svc = "live-generator"
	}
	rec := maping.NewRecorder(
		maping.WithKey("dev-key"),
		maping.WithEndpoint(endpoint),
		maping.WithService(svc),
		maping.WithFlushWindow(2*time.Second),
	)
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(mapinggin.MiddlewareWithRecorder(rec))
	r.Use(gin.Recovery())
	r.GET("/healthz", func(c *gin.Context) { c.String(200, "ok") })
	r.GET("/", func(c *gin.Context) { c.String(200, "home") })
	r.GET("/login", func(c *gin.Context) { c.String(200, "login") })
	r.GET("/dashboard", func(c *gin.Context) { c.String(200, "dash") })
	r.GET("/goals", func(c *gin.Context) { c.String(200, "goals") })
	r.GET("/projects/:id", func(c *gin.Context) { c.String(200, "proj") })
	r.GET("/profile/work-style/:name", func(c *gin.Context) { c.Redirect(302, "/") })
	r.POST("/form/submit/login", func(c *gin.Context) { c.String(200, "ok") })
	r.GET("/api/v1/missing", func(c *gin.Context) { c.String(404, "nope") })
	r.GET("/api/v1/boom", func(c *gin.Context) { c.String(500, "boom") })
	// Mount the /fault/* test-bed so a selected fault can be driven for a live,
	// diagnosable anomaly. Harmless when idle: unmatched faults are never called.
	faults.Register(r)
	app := httptest.NewServer(r)
	defer app.Close()
	cl := app.Client()

	type ep struct {
		m, p string
		w    int
	}
	eps := []ep{{"GET", "/healthz", 6}, {"GET", "/", 4}, {"GET", "/login", 3}, {"GET", "/dashboard", 5},
		{"GET", "/goals", 3}, {"GET", "/projects/491", 4}, {"GET", "/profile/work-style/keystone", 2},
		{"POST", "/form/submit/login", 2}, {"GET", "/api/v1/missing", 1}, {"GET", "/api/v1/boom", 1}}

	// GEN_FAULT selects one fault to drive alongside the healthy baseline. It is
	// weighted heavily so the anomaly is unmistakable on the dashboard.
	if name := os.Getenv("GEN_FAULT"); name != "" {
		path := "/fault/" + name
		if params := os.Getenv("GEN_FAULT_PARAMS"); params != "" {
			path += "?" + params
		}
		eps = append(eps, ep{"GET", path, 8})
		log.Printf("driving fault %q at %s", name, path)
	}

	var bag []ep
	for _, e := range eps {
		for i := 0; i < e.w; i++ {
			bag = append(bag, e)
		}
	}
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ticker := time.NewTicker(40 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = rec.Shutdown(sctx)
			cancel()
			return
		case <-ticker.C:
			e := bag[rng.Intn(len(bag))]
			if e.m == "POST" {
				resp, err := cl.Post(app.URL+e.p, "text/plain", nil)
				if err == nil {
					resp.Body.Close()
				}
			} else {
				resp, err := cl.Get(app.URL + e.p)
				if err == nil {
					resp.Body.Close()
				}
			}
		}
	}
}
