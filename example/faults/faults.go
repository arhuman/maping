// Package faults is the mAPI-ng diagnostic test-bed: a catalog of deliberately
// misbehaving HTTP endpoints, one per diagnostic dimension the dashboard claims
// to detect. Hitting a fault produces REAL runtime signals — CPU burn, latency
// spread, error rates, no-status aborts, leaking goroutines, downstream time —
// so the dashboard's verdicts and diagnoses can be validated against ground
// truth instead of hand-crafted synthetic data.
//
// Every fault lives under /fault/* (deliberate misbehavior, self-documenting)
// and takes intensity knobs as query params, e.g. /fault/busy?ms=50,
// /fault/flaky?pct=25. Stateful faults (rampcpu, creep, goroutineleak)
// accumulate; /fault/reset clears them so a demo can start from a clean slate.
//
// Faults are process-GLOBAL by design: a goroutine leak or CPU ramp poisons the
// whole instance, not one route — which mirrors how real resource pressure is a
// property of the process, shared by every endpoint it serves. Drive one fault
// at a time (or one per instance) for a clean signal.
//
// This batch covers only the dimensions observable from data mAPI-ng already
// collects (the "✅ now" column of the plan's coverage matrix). The memory
// faults (leak/spike/churn/bloat) land in a later batch, alongside the
// post-GC-heap instrumentation that makes them legible.
package faults

import (
	"context"
	"errors"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"

	maping "github.com/arhuman/maping/client"
)

// Register mounts every /fault/* endpoint on r. Safe to call once per router;
// the shared state (ramps, leaked goroutines) is package-global so it behaves as
// a single process-wide fault surface regardless of how many routers mount it.
func Register(r gin.IRouter) {
	g := r.Group("/fault")
	g.GET("/busy", handleBusy)
	g.GET("/rampcpu", handleRampCPU)
	g.GET("/sometimesbusy", handleSometimesBusy)
	g.GET("/creep", handleCreep)
	g.GET("/jitter", handleJitter)
	g.GET("/flaky", handleFlaky)
	g.GET("/throttle", handleThrottle)
	g.GET("/timeout", handleTimeout)
	g.GET("/panic", handlePanic)
	g.GET("/goroutineleak", handleGoroutineLeak)
	g.GET("/downstream", handleDownstream)
	g.GET("/boom", handleBoom)
	g.GET("/reset", handleReset)
}

// qint reads a query param as an int, clamped to [lo, hi], falling back to def
// when absent or unparseable. Keeps every handler's knob handling one line and
// bounds runaway inputs (a demo must not be able to burn 10 minutes of CPU).
func qint(c *gin.Context, key string, def, lo, hi int) int {
	v := def
	if n, err := strconv.Atoi(c.Query(key)); err == nil {
		v = n
	}
	return min(max(v, lo), hi)
}

// --- CPU faults ------------------------------------------------------------

// burnSink defeats dead-code elimination of the busy loops below.
var burnSink atomic.Uint64

// burn spins on real arithmetic for d, consuming a CPU core the whole time. This
// is deliberately NOT a sleep: it shows up as cpu_ns (cores-used), not as idle
// wall time, which is exactly the CPU-saturation signal the dashboard grades.
func burn(d time.Duration) {
	if d <= 0 {
		return
	}
	deadline := time.Now().Add(d)
	x := 1.000001
	for time.Now().Before(deadline) {
		for i := 0; i < 50000; i++ {
			x = math.Sqrt(x*x + 1.0000001)
		}
	}
	burnSink.Store(math.Float64bits(x))
}

// handleBusy pins a CPU core for a fixed slice each call. Dimension: CPU
// cores-used. Knob: ?ms=25.
func handleBusy(c *gin.Context) {
	ms := qint(c, "ms", 25, 0, 5000)
	burn(time.Duration(ms) * time.Millisecond)
	c.JSON(http.StatusOK, gin.H{"burned_ms": ms})
}

// rampCPUns is the current per-call burn budget for /fault/rampcpu; it climbs by
// ?step each call, so cores-used trends upward over time — a regression, not a
// spike. Reset via /fault/reset. Clamped so a long demo plateaus instead of
// consuming the whole box.
var rampCPUns atomic.Int64

const rampCPUMaxNs = int64(250 * time.Millisecond)

func handleRampCPU(c *gin.Context) {
	step := int64(qint(c, "step", 1, 0, 1000)) * int64(time.Millisecond)
	cur := rampCPUns.Add(step)
	if cur > rampCPUMaxNs {
		cur = rampCPUMaxNs
		rampCPUns.Store(rampCPUMaxNs)
	}
	burn(time.Duration(cur))
	c.JSON(http.StatusOK, gin.H{"burn_ns": cur})
}

// sometimesCount drives the 1-in-N selection for /fault/sometimesbusy.
var sometimesCount atomic.Uint64

// handleSometimesBusy burns a core on 1 call in N and returns fast otherwise, so
// p50 stays flat while p95 and the p95/p50 spread blow up — the "tail problem"
// signal. Knobs: ?n=10 (one in ten heavy), ?ms=50 (how heavy).
func handleSometimesBusy(c *gin.Context) {
	n := uint64(qint(c, "n", 10, 1, 1000000))
	if sometimesCount.Add(1)%n == 0 {
		burn(time.Duration(qint(c, "ms", 50, 0, 5000)) * time.Millisecond)
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// --- latency faults --------------------------------------------------------

// creepNs is the current sleep for /fault/creep; it climbs by ?step each call so
// latency drifts up slowly over minutes — the latency-vs-baseline signal that a
// single spike would not exercise. Reset via /fault/reset.
var creepNs atomic.Int64

const creepMaxNs = int64(500 * time.Millisecond)

func handleCreep(c *gin.Context) {
	step := int64(qint(c, "step", 2, 0, 1000)) * int64(time.Millisecond)
	cur := creepNs.Add(step)
	if cur > creepMaxNs {
		cur = creepMaxNs
		creepNs.Store(creepMaxNs)
	}
	time.Sleep(time.Duration(cur))
	c.JSON(http.StatusOK, gin.H{"slept_ns": cur})
}

// handleJitter sleeps a random duration in [min,max] ms, producing latency
// spread without a rising trend. Dimension: spread. Knobs: ?min=1&max=100.
func handleJitter(c *gin.Context) {
	lo := qint(c, "min", 1, 0, 10000)
	hi := qint(c, "max", 100, 0, 10000)
	if hi < lo {
		hi = lo
	}
	d := lo
	if hi > lo {
		d = lo + rand.Intn(hi-lo+1)
	}
	time.Sleep(time.Duration(d) * time.Millisecond)
	c.JSON(http.StatusOK, gin.H{"slept_ms": d})
}

// --- error / status faults -------------------------------------------------

// handleFlaky fails a percentage of calls with a labelled 5xx, driving the error
// rate verdict and the error-class breakdown. Knob: ?pct=10.
func handleFlaky(c *gin.Context) {
	if rand.Intn(100) < qint(c, "pct", 10, 0, 100) {
		_ = c.Error(errors.New("flaky backend")) // -> FLAKY_BACKEND error class
		c.JSON(http.StatusInternalServerError, gin.H{"error": "flaky"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// handleThrottle returns 429 for a percentage of calls, exercising the 4xx
// status class. Knob: ?pct=100 (rate-limit everything by default).
func handleThrottle(c *gin.Context) {
	if rand.Intn(100) < qint(c, "pct", 100, 0, 100) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "rate limited"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// handleTimeout lets the request deadline fire before any status is written, so
// the adapter records NO_STATUS / CONTEXT_DEADLINE — the overload/timeout signal.
// Knob: ?ms=5 (deadline).
func handleTimeout(c *gin.Context) {
	d := time.Duration(qint(c, "ms", 5, 0, 60000)) * time.Millisecond
	ctx, cancel := context.WithTimeout(c.Request.Context(), d)
	defer cancel()
	c.Request = c.Request.WithContext(ctx)
	<-ctx.Done() // deadline fires -> NO_STATUS / CONTEXT_DEADLINE
}

// handlePanic panics; the host's gin.Recovery converts it to a 500, which the
// middleware (mounted above Recovery) observes as a 5xx. Dimension: error verdict
// from panics.
func handlePanic(_ *gin.Context) {
	panic("fault: deliberate panic")
}

// handleBoom returns 100% 5xx — the unambiguous "broken" endpoint. Distinct from
// flaky (partial) and panic (via recovery): a clean, always-failing verdict.
func handleBoom(c *gin.Context) {
	_ = c.Error(errors.New("boom"))
	c.JSON(http.StatusInternalServerError, gin.H{"error": "boom"})
}

// --- goroutine fault -------------------------------------------------------

// leakQuit is closed by /fault/reset to release every goroutine leaked so far;
// it is replaced with a fresh channel so subsequent leaks accumulate again.
// leakCount tracks how many are currently parked (for the reset response).
var (
	leakMu    sync.Mutex
	leakQuit  = make(chan struct{})
	leakCount int
)

// handleGoroutineLeak parks a goroutine forever (until reset) on each call, so
// runtime.NumGoroutine climbs monotonically — the goroutine-leak trend, as
// opposed to a spike that returns to baseline. Knob: ?n=1 (leak n per call).
func handleGoroutineLeak(c *gin.Context) {
	n := qint(c, "n", 1, 1, 1000)
	leakMu.Lock()
	quit := leakQuit
	leakCount += n
	total := leakCount
	leakMu.Unlock()
	for i := 0; i < n; i++ {
		go func() { <-quit }()
	}
	c.JSON(http.StatusOK, gin.H{"leaked_total": total})
}

// --- downstream fault ------------------------------------------------------

// dsClient wraps the default transport in a maping RoundTripper so the time its
// outbound request spends waiting is attributed to the calling endpoint — the
// self-vs-downstream split. dsSleepy is a lazily-started in-process stand-in for
// a slow dependency, so the fault is self-contained (no external service, no
// host base-URL plumbing).
var (
	dsClient = &http.Client{Transport: maping.NewRoundTripper(nil)}
	dsOnce   sync.Once
	dsSleepy *httptest.Server
)

func sleepyURL() string {
	dsOnce.Do(func() {
		dsSleepy = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ms, err := strconv.Atoi(req.URL.Query().Get("ms"))
			if err != nil || ms < 0 {
				ms = 40
			}
			time.Sleep(time.Duration(ms) * time.Millisecond)
			w.WriteHeader(http.StatusOK)
		}))
	})
	return dsSleepy.URL
}

// handleDownstream makes a slow outbound call through the maping RoundTripper.
// Its wait lands as downstream time on this endpoint, so latency is high while
// self-time stays low — the signal that rules the process itself out. Knob:
// ?ms=40 (downstream latency).
func handleDownstream(c *gin.Context) {
	ms := qint(c, "ms", 40, 0, 60000)
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet,
		sleepyURL()+"/?ms="+strconv.Itoa(ms), nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "build request"})
		return
	}
	resp, err := dsClient.Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "downstream failed"})
		return
	}
	_ = resp.Body.Close()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// --- reset -----------------------------------------------------------------

// handleReset clears all stateful faults: it zeroes the CPU/latency ramps and
// releases every leaked goroutine, so a demo can restart the staircase from a
// clean baseline. Per-request faults (busy, jitter, flaky, ...) hold no state.
func handleReset(c *gin.Context) {
	rampCPUns.Store(0)
	creepNs.Store(0)
	sometimesCount.Store(0)
	leakMu.Lock()
	released := leakCount
	close(leakQuit)
	leakQuit = make(chan struct{})
	leakCount = 0
	leakMu.Unlock()
	c.JSON(http.StatusOK, gin.H{
		"reset":               true,
		"goroutines_released": released,
	})
}
