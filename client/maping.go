// Package maping is the framework-agnostic Core of the mAPI-ng client: hosted,
// zero-config API observability for Go services (see docs/context.md). It owns
// config/env parsing, the in-process Summary aggregation (counters + a latency
// DDSketch per series), and the background Connect uploader.
//
// The guiding contract is zero-config (CONFIG.md): the only required input is
// the ingest key. With no key resolved, NewRecorder returns a no-op recorder
// so adding mAPI-ng to a codebase is always safe — activation is a matter of
// flipping an env var, decoupled from the code change. The client fails open:
// setup and upload problems are logged (rate-limited) and surfaced in the
// dashboard, but never panic or block the host.
//
// A framework adapter (e.g. client/gin) extracts the route template and final
// status after each request and calls Observe with a neutral Record.
package maping

import (
	"context"
	"log/slog"
	"maps"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	mapingv1 "github.com/arhuman/maping/proto/maping/v1"

	"github.com/arhuman/maping/client/internal/buffer"
	"github.com/arhuman/maping/client/internal/transport"
	"github.com/arhuman/maping/client/sketch"
)

// SdkVersion is the client SDK version reported in every Envelope/Handshake for
// server-side compatibility handling.
const SdkVersion = "0.1.0"

// firstFlushDelay is the accelerated first flush on cold start so first data
// reaches the dashboard in seconds before reverting to the flush window.
const firstFlushDelay = 2 * time.Second

// numShards is the fixed shard count for the hot-path aggregation map. It must
// be a power of two so shard selection is a mask (hash & shardMask) rather than
// a modulo. Sharding cuts lock contention under concurrent request load: each
// request touches only the one shard its series hashes to.
const (
	numShards = 16
	shardMask = numShards - 1
)

// Retry/backoff bounds for the fail-open uploader. A down collector must never
// block flushing new windows or the host, so failed uploads are retried on an
// independent cadence with exponential backoff between attempts.
const (
	baseBackoff = 1 * time.Second
	maxBackoff  = 30 * time.Second
	// drainRetryDelay paces retries during graceful Shutdown so a transient send
	// failure is retried within the caller's ctx budget without a tight spin.
	drainRetryDelay = 50 * time.Millisecond
)

// minRingCapacity is the floor for the pending-upload ring: even a very long
// flush window keeps a few windows of headroom before drop-oldest kicks in.
const minRingCapacity = 8

// ringWindowsSpan is how many seconds of windows the ring is sized to hold
// (roughly five minutes) before it starts dropping the oldest pending upload.
const ringWindowsSpan = 300

// Uploader is the transport dependency the Recorder needs. The concrete
// implementation lives in internal/transport; adapters and tests may inject a
// fake to observe uploads without a live collector (lang-go DI).
type Uploader interface {
	Upload(ctx context.Context, req *mapingv1.UploadRequest) error
	Register(ctx context.Context, hs *mapingv1.Handshake) error
}

// Recorder aggregates request Records in process and ships Summaries on a flush
// cycle. A recorder with no resolved key is a no-op: every method is safe and
// does nothing, and no goroutine runs.
type Recorder struct {
	cfg      Config
	tx       Uploader
	logDedup sync.Once
	warnOnce sync.Once // rate-limits the Observe panic-recovery Warn log

	// shards partition the hot-path aggregation map to cut lock contention. A
	// non-active (no-op) recorder leaves this nil and never touches it.
	shards *[numShards]shard

	// ring buffers built UploadRequests pending a successful upload. It is owned
	// solely by the uploader goroutine (and, after run() exits, by Shutdown), so
	// it needs no locking of its own — see internal/buffer.
	ring *buffer.Ring

	// droppedSummaries counts summaries lost to ring overflow since the last
	// successful upload, stamped into the next envelope's dropped_summaries.
	// Read/written only on the uploader goroutine (single-owner, no atomic
	// needed) — kept as a plain field alongside the ring.
	droppedSummaries uint64

	// closed guards against Observe racing with Shutdown after the aggregation
	// state has been drained: once set, Observe is a cheap no-op.
	closed atomic.Bool

	stop     chan struct{}
	stopOnce sync.Once
	done     chan struct{}
}

// NewRecorder resolves config and returns a Recorder. If the resolved key is
// empty (zero-config safety), it returns a no-op recorder with no goroutine. On
// a transport construction failure it logs once at Warn and also returns the
// no-op recorder — the host is never affected (CONFIG.md fail-open).
func NewRecorder(opts ...Option) *Recorder {
	cfg := resolveConfig(opts)
	if cfg.Key == "" {
		return &Recorder{} // no-op
	}
	tx, err := transport.New(cfg.Endpoint, cfg.Key)
	if err != nil {
		slog.Warn("maping: invalid transport config, disabling recorder", "endpoint", cfg.Endpoint, "err", err)
		return &Recorder{} // no-op
	}
	return newRecorderWithTransport(cfg, tx)
}

// NewRecorderForTest returns a running Recorder wired to an injected Uploader,
// with a minimal config. It is the seam adapter tests use to substitute a fake
// transport and assert what was observed, without a live collector.
func NewRecorderForTest(tx Uploader) *Recorder {
	return newRecorderWithTransport(
		Config{Service: "test", Instance: "test", FlushWindow: time.Second},
		tx,
	)
}

// newRecorderWithTransport wires a running recorder around an injected uploader.
// It is the seam tests use to substitute a fake transport.
func newRecorderWithTransport(cfg Config, tx Uploader) *Recorder {
	shards := new([numShards]shard)
	for i := range shards {
		shards[i].m = make(map[seriesKey]*series)
	}
	r := &Recorder{
		cfg:    cfg,
		tx:     tx,
		shards: shards,
		ring:   buffer.New(ringCapacity(cfg.FlushWindow)),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	go r.run()
	return r
}

// ringCapacity sizes the pending-upload ring to roughly five minutes of windows
// (ringWindowsSpan), with a small floor so short windows still keep headroom.
func ringCapacity(flushWindow time.Duration) int {
	secs := max(int(flushWindow/time.Second), 1)
	return max(minRingCapacity, ringWindowsSpan/secs)
}

// active reports whether this recorder does real work (has a running goroutine).
func (r *Recorder) active() bool { return r.tx != nil }

// Observe records one completed request. It is safe on a no-op recorder and
// safe for concurrent use.
//
// The whole body is wrapped in a panic recovery: a bug in aggregation must be
// invisible to the host request (the core "mAPI-ng failing is invisible to the
// host" guarantee, docs/context.md). A recovered panic is logged once at Warn
// (rate-limited) and the observation is dropped.
//
// Steady state (the series already exists) is allocation-free: shard select +
// lock + in-place counter increments + sketch.Add (itself alloc-free). Only the
// first sighting of a new series allocates (map insert + sketch.New).
func (r *Recorder) Observe(rec Record) {
	if !r.active() || r.closed.Load() {
		return
	}
	defer func() {
		if p := recover(); p != nil {
			r.warnOnce.Do(func() {
				slog.Warn("maping: recovered panic in Observe (further panics suppressed)", "panic", p)
			})
		}
	}()

	key := seriesKey{method: rec.Method, route: rec.RouteTemplate, class: classify(rec.Status)}
	sh := &r.shards[key.hash()&shardMask]

	sh.mu.Lock()
	defer sh.mu.Unlock()
	s := sh.m[key]
	if s == nil {
		s = &series{sk: sketch.New(), codes: make(map[uint32]uint64)}
		sh.m[key] = s
	}
	s.count++
	if rec.Duration > 0 {
		s.sumDurationNs += uint64(rec.Duration.Nanoseconds())
	}
	if rec.ReqBytes > 0 {
		s.reqBytes += uint64(rec.ReqBytes)
	}
	if rec.RespBytes > 0 {
		s.respBytes += uint64(rec.RespBytes)
	}
	s.sk.Add(rec.Duration.Seconds())
	recordCode(s.codes, rec.Status)
}

// recordCode bumps an exact status code, bounded to maxStatusCodes distinct
// codes. A new code past the cap is dropped rather than growing unbounded.
func recordCode(codes map[uint32]uint64, status int) {
	if status <= 0 {
		return
	}
	code := uint32(status)
	if _, ok := codes[code]; !ok && len(codes) >= maxStatusCodes {
		return
	}
	codes[code]++
}

// run is the background uploader loop. It sends the startup Handshake, then on
// two independent cadences: a flush timer that swaps each window into the ring,
// and a retry timer that drains the oldest pending upload with exponential
// backoff. Separating them means a slow or down collector never blocks flushing
// new windows or the host (docs/context.md). The whole loop is wrapped in a
// panic recovery so an internal bug can never crash the host process.
//
//nolint:gocognit,gocyclo // flush/retry/backoff/drain event loop; the branch count is inherent to the recorder's states.
func (r *Recorder) run() {
	defer close(r.done)
	defer func() {
		if p := recover(); p != nil {
			slog.Warn("maping: recovered panic in uploader loop", "panic", p, "stack", string(debug.Stack()))
		}
	}()

	regCtx, cancel := context.WithTimeout(context.Background(), r.cfg.FlushWindow)
	if err := r.tx.Register(regCtx, r.handshake()); err != nil {
		slog.Debug("maping: register failed", "err", err)
	}
	cancel()

	flushTimer := time.NewTimer(firstFlushDelay)
	defer flushTimer.Stop()

	// retryTimer drives ring drain attempts. It starts stopped; a Push arms it,
	// and each drain attempt re-arms it (immediately on success to drain more,
	// after a backoff on failure).
	retryTimer := time.NewTimer(0)
	if !retryTimer.Stop() {
		<-retryTimer.C
	}
	backoff := baseBackoff

	for {
		select {
		case <-r.stop:
			return
		case <-flushTimer.C:
			if r.flush() && r.ring.Len() == 1 {
				// First pending item since the ring drained empty: kick a drain
				// attempt now rather than waiting for a prior backoff to elapse.
				resetTimer(retryTimer, 0)
			}
			flushTimer.Reset(r.cfg.FlushWindow)
		case <-retryTimer.C:
			switch r.tryDrainOldest(context.Background()) {
			case drainSent:
				backoff = baseBackoff
				if r.ring.Len() > 0 {
					resetTimer(retryTimer, 0) // more pending: drain again immediately
				}
			case drainFailed:
				resetTimer(retryTimer, backoff)
				backoff = nextBackoff(backoff)
			case drainEmpty:
				// nothing to do; a future Push re-arms the timer
			}
		}
	}
}

// drainResult is the outcome of one ring-drain attempt.
type drainResult int

const (
	drainEmpty  drainResult = iota // ring was empty; nothing sent
	drainSent                      // oldest request sent and removed
	drainFailed                    // send failed; request left in the ring
)

// tryDrainOldest attempts to send the OLDEST pending request. On success it
// stamps and removes it; on failure it leaves it in the ring for a later retry.
func (r *Recorder) tryDrainOldest(ctx context.Context) drainResult {
	req := r.ring.Peek()
	if req == nil {
		return drainEmpty
	}
	if err := r.sendStamped(ctx, req); err != nil {
		r.logDedup.Do(func() {
			slog.Debug("maping: upload failed (backing off, further errors suppressed)", "err", err)
		})
		return drainFailed
	}
	r.ring.PopOldest()
	return drainSent
}

// sendStamped stamps the request's dropped_summaries with the current dropped
// counter at SEND time (not build time), so the number reflects everything lost
// up to the moment of this upload. On success it subtracts the stamped amount
// from the counter, so the next successful envelope truthfully reports only the
// gap since this one (dropped_summaries = "dropped since last successful
// upload", docs/context.md).
func (r *Recorder) sendStamped(ctx context.Context, req *mapingv1.UploadRequest) error {
	stamped := r.droppedSummaries
	req.Envelope.DroppedSummaries = stamped
	if err := r.tx.Upload(ctx, req); err != nil {
		return err
	}
	r.droppedSummaries -= stamped
	return nil
}

// resetTimer safely resets a timer that may or may not have already fired,
// draining a pending tick if present so the reset does not race a stale value.
func resetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}

// nextBackoff doubles the current backoff up to maxBackoff.
func nextBackoff(cur time.Duration) time.Duration {
	next := cur * 2
	if next > maxBackoff {
		return maxBackoff
	}
	return next
}

// handshake builds the one-time registration ping.
func (r *Recorder) handshake() *mapingv1.Handshake {
	return &mapingv1.Handshake{
		Service:             r.cfg.Service,
		Instance:            r.cfg.Instance,
		SdkVersion:          SdkVersion,
		SketchFormatVersion: sketch.SketchFormatVersion,
	}
}

// flush double-buffers every shard (swapping each map out under its own lock so
// the hot path is never blocked on the merge/build), merges the swapped shards
// into one UploadRequest, and pushes it onto the ring for the drain loop to
// send. It never blocks on I/O. It reports whether a non-empty request was
// pushed.
//
// On ring overflow the oldest pending request is dropped; its summary count is
// added to the dropped counter so the next successful envelope reports the gap.
// flush is called only from the uploader goroutine (via run) or from Shutdown
// after that goroutine has exited, so the ring stays single-owner.
func (r *Recorder) flush() bool {
	windowEnd := time.Now()
	swapped := r.swapShards()
	if len(swapped) == 0 {
		return false
	}

	req := r.buildRequest(swapped, windowEnd)
	if evicted := r.ring.Push(req); evicted != nil {
		r.droppedSummaries += uint64(len(evicted.Summaries))
		r.logDedup.Do(func() {
			slog.Debug("maping: ring full, dropped oldest pending upload (further drops suppressed)")
		})
	}
	return true
}

// swapShards double-buffers each shard and merges the swapped maps into one
// window. Each shard is swapped under its own lock, so a concurrent Observe on
// another shard never contends. Same-key series cannot appear in two shards (a
// key always hashes to the same shard), so no per-series merge is needed.
func (r *Recorder) swapShards() map[seriesKey]*series {
	merged := make(map[seriesKey]*series)
	for i := range r.shards {
		sh := &r.shards[i]
		sh.mu.Lock()
		old := sh.m
		if len(old) > 0 {
			sh.m = make(map[seriesKey]*series)
		}
		sh.mu.Unlock()
		maps.Copy(merged, old)
	}
	return merged
}

// buildRequest turns a swapped window into an UploadRequest. window_start/end
// are client wall-clock stamps (docs/context.md); the window is treated as ending
// now and having started one flush window earlier.
func (r *Recorder) buildRequest(window map[seriesKey]*series, end time.Time) *mapingv1.UploadRequest {
	startMs := end.Add(-r.cfg.FlushWindow).UnixMilli()
	endMs := end.UnixMilli()

	summaries := make([]*mapingv1.Summary, 0, len(window))
	for key, s := range window {
		summaries = append(summaries, &mapingv1.Summary{
			WindowStartMs:       startMs,
			WindowEndMs:         endMs,
			Method:              key.method,
			RouteTemplate:       key.route,
			StatusClass:         key.class,
			Count:               s.count,
			SumDurationNs:       s.sumDurationNs,
			ReqBytes:            s.reqBytes,
			RespBytes:           s.respBytes,
			LatencySketch:       s.sk.Buckets(),
			StatusCodeBreakdown: s.codes,
		})
	}

	return &mapingv1.UploadRequest{
		Envelope: &mapingv1.Envelope{
			Service:             r.cfg.Service,
			Instance:            r.cfg.Instance,
			SdkVersion:          SdkVersion,
			SketchFormatVersion: sketch.SketchFormatVersion,
			// DroppedSummaries is stamped at send time from the live counter, not
			// here at build time, so it reflects everything dropped up to the
			// upload (see sendStamped).
		},
		Summaries: summaries,
	}
}

// Shutdown stops the uploader goroutine, does a final flush (pushing the last
// window onto the ring), then synchronously drains the ring — attempting to
// send every pending request, bounded by ctx. A graceful shutdown therefore
// does not lose buffered summaries: it best-effort ships them before returning
// (this is what the E2E test relies on). Once ctx expires it stops retrying and
// returns; any still-pending requests are abandoned. It is synchronous and
// idempotent. Hosts must call it AFTER their http.Server.Shutdown so no request
// is still writing a Record.
func (r *Recorder) Shutdown(ctx context.Context) error {
	if !r.active() {
		return nil
	}
	r.stopOnce.Do(func() { close(r.stop) })
	<-r.done // uploader goroutine has exited; the ring is now solely ours

	// Bar further observations so late Records don't race the final drain, then
	// flush the last window into the ring.
	r.closed.Store(true)
	r.flush()

	// Drain the ring oldest-first until empty or ctx expires. On a transient
	// failure (e.g. an h2c connection warming up on the first send), wait briefly
	// and retry within the ctx budget rather than abandoning buffered summaries —
	// graceful shutdown must make a real effort to ship what it has, bounded by
	// the caller's deadline so it never overruns.
	for r.ring.Len() > 0 && ctx.Err() == nil {
		if r.tryDrainOldest(ctx) == drainFailed {
			select {
			case <-ctx.Done():
			case <-time.After(drainRetryDelay):
			}
		}
	}
	return ctx.Err()
}
