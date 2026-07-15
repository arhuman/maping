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

	// startTime is the process boot wall-clock, captured once at recorder
	// creation and stamped into every Envelope's instance_start_time_ms so
	// restarts can be correlated with a change in behavior.
	startTime time.Time

	// shards partition the hot-path aggregation map to cut lock contention. A
	// non-active (no-op) recorder leaves this nil and never touches it.
	shards *[numShards]shard

	// sampler captures per-window process resource gauges (USE) attached to each
	// upload. Touched only by the uploader goroutine, so it needs no locking.
	sampler sampler

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
		cfg:       cfg,
		tx:        tx,
		startTime: time.Now(),
		shards:    shards,
		ring:      buffer.New(ringCapacity(cfg.FlushWindow)),
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
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
		s = &series{
			sk:              sketch.New(),
			codes:           make(map[uint32]uint64),
			errorClasses:    make(map[string]uint64),
			noStatusReasons: make(map[uint32]uint64),
		}
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
	recordErrorClass(s.errorClasses, rec.ErrorClass)
	if rec.Status <= 0 {
		recordNoStatusReason(s.noStatusReasons, rec.NoStatusReason)
	}
	if rec.DownstreamDuration > 0 {
		s.sumDownstreamNs += uint64(rec.DownstreamDuration.Nanoseconds())
	}
	s.observeExemplar(rec, exemplarOf(rec, time.Now()))
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
