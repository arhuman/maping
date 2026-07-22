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
// The memory faults (leak/spike/churn/bloat) move the post-GC live heap and the
// allocation rate/size, so they are legible only alongside the post-GC-heap and
// true-RSS instrumentation that ships with them.
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
	g.GET("/leak", handleLeak)
	g.GET("/spike", handleSpike)
	g.GET("/churn", handleChurn)
	g.GET("/bloat", handleBloat)
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

// --- memory faults ---------------------------------------------------------
//
// These are the leak-vs-burst discrimination pair plus the allocation-shape pair.
// They move REAL runtime signals: retained allocations raise the post-GC live
// heap (a staircase for leak, a sawtooth for spike), and short-lived allocations
// drive the allocation rate and average allocation size (tiny-and-many for churn,
// few-and-huge for bloat). All are process-global, matching how heap pressure is
// a property of the instance. `held` is the only retained state; /fault/reset
// frees it.

// memSink defeats dead-code elimination of the transient allocations below: every
// fault XORs a byte from what it allocates into it, so the compiler cannot prove
// the allocation is unused and elide it.
var memSink atomic.Uint64

// touch writes to the first byte of every 4 KB page of b and folds one byte back
// into memSink, forcing the pages to be committed (so RSS actually moves) and the
// allocation to escape.
func touch(b []byte) {
	var acc byte
	for i := 0; i < len(b); i += 4096 {
		b[i] = byte(i)
		acc ^= b[i]
	}
	if len(b) > 0 {
		acc ^= b[len(b)-1]
	}
	memSink.Add(uint64(acc))
}

// leakNode is a tiny pointer-bearing object. A retained []byte holds no pointers,
// so the GC skips scanning its contents and a byte-only leak moves the heap
// without ever touching latency. Retaining a large graph of leakNodes instead
// makes every GC mark cycle walk millions of live pointers, so GC CPU climbs and
// allocating goroutines pay mark-assist — the leak now degrades latency as it grows.
type leakNode struct{ next *leakNode }

// held is the leaked memory for /fault/leak: byte buffers plus pointer-graph
// objects retained forever (until reset), so the post-GC live heap climbs
// monotonically and GC mark cost climbs with it. heldBytes tracks the byte total
// for the response.
var (
	heldMu    sync.Mutex
	held      [][]byte
	heldNodes [][]*leakNode
	heldBytes int64
)

// handleLeak retains kb KiB of byte heap on each call, never freeing it, so the
// post-GC live heap grows as a staircase — the signature of a leak (vs the sawtooth
// of a burst). It also retains objs pointer-bearing objects (default scales with kb)
// so GC mark cost, and thus latency under allocation, grows with the leak. Knobs:
// ?kb=256, ?objs. Released by /fault/reset.
func handleLeak(c *gin.Context) {
	kb := qint(c, "kb", 256, 0, 1024*1024)
	b := make([]byte, kb*1024)
	touch(b)
	objs := qint(c, "objs", kb*128, 0, 64*1024*1024)
	nodes := make([]*leakNode, objs)
	for i := range nodes {
		nodes[i] = &leakNode{}
		if i > 0 {
			nodes[i].next = nodes[i-1] // a real live-pointer chain for the GC to walk
		}
	}
	heldMu.Lock()
	held = append(held, b)
	heldNodes = append(heldNodes, nodes)
	heldBytes += int64(len(b))
	total := heldBytes
	depth := len(heldNodes) // how many times we have leaked so far
	heldMu.Unlock()

	// A WORSENING leak: per-request transient garbage scales with how much has
	// already leaked (churn>0 enables it), so alloc rate, GC frequency and GC CPU
	// ramp up as the heap grows — instead of a lone heap-climb signal, the incident
	// correlates four runtime signals at once (the High-confidence, hard-for-a-human
	// case). churn=0 keeps the pure retain-only leak (a single-signal case).
	if churn := qint(c, "churn", 0, 0, 100000); churn > 0 {
		reps := depth * churn
		if reps > 500000 {
			reps = 500000 // clamp so a long demo plateaus instead of exploding
		}
		garbage := make([][]byte, reps)
		var acc byte
		for i := range garbage {
			garbage[i] = make([]byte, 64)
			garbage[i][0] = byte(i)
			acc ^= garbage[i][0]
		}
		memSink.Add(uint64(acc)) // defeat dead-code elimination; garbage drops here
	}
	c.JSON(http.StatusOK, gin.H{"held_bytes": total, "held_objs": objs, "depth": depth})
}

// handleSpike allocates mb MiB, touches it, then drops it (no retention), so the
// heap sawtooths up and falls back to baseline after the next GC — a burst, not a
// leak. The crown-jewel contrast with /fault/leak. Knob: ?mb=64.
func handleSpike(c *gin.Context) {
	mb := qint(c, "mb", 64, 0, 4096)
	b := make([]byte, mb*1024*1024)
	touch(b)
	// b goes out of scope here: reclaimable at the next GC.
	c.JSON(http.StatusOK, gin.H{"spiked_bytes": len(b)})
}

// handleChurn makes n short-lived tiny allocations per call, so the allocation
// rate and allocation COUNT (mallocs) spike while the live heap stays flat and the
// average allocation size stays small — many small objects. Knobs: ?n=200000,
// ?sz=32 (bytes each). The buffers are stored into a heap-allocated slice (so
// escape analysis cannot elide them onto the stack — that is what makes them count
// as real heap allocations); the whole batch is dropped when the handler returns.
func handleChurn(c *gin.Context) {
	n := qint(c, "n", 200000, 0, 5000000)
	sz := qint(c, "sz", 32, 1, 4096)
	hold := make([][]byte, n)
	for i := 0; i < n; i++ {
		b := make([]byte, sz)
		b[0] = byte(i)
		hold[i] = b
	}
	var acc byte
	for _, b := range hold {
		acc ^= b[0]
	}
	memSink.Add(uint64(acc))
	c.JSON(http.StatusOK, gin.H{"allocs": n, "size": sz})
}

// handleBloat makes a few very large short-lived allocations per call, so the
// allocation rate is high but the allocation COUNT is low — the average allocation
// size blows up (large objects), the opposite shape from churn. Knobs: ?mb=16,
// ?count=4.
func handleBloat(c *gin.Context) {
	mb := qint(c, "mb", 16, 1, 4096)
	count := qint(c, "count", 4, 1, 1024)
	for i := 0; i < count; i++ {
		b := make([]byte, mb*1024*1024)
		touch(b) // b dropped next iteration
	}
	c.JSON(http.StatusOK, gin.H{"count": count, "mb_each": mb})
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
	heldMu.Lock()
	freed := heldBytes
	held = nil
	heldNodes = nil
	heldBytes = 0
	heldMu.Unlock()
	c.JSON(http.StatusOK, gin.H{
		"reset":               true,
		"goroutines_released": released,
		"heap_freed_bytes":    freed,
	})
}
