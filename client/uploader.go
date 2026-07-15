package maping

import (
	"context"
	"log/slog"
	"maps"
	"runtime/debug"
	"time"

	mapingv1 "github.com/arhuman/maping/proto/maping/v1"

	"github.com/arhuman/maping/client/sketch"
)

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
			WindowStartMs:           startMs,
			WindowEndMs:             endMs,
			Method:                  key.method,
			RouteTemplate:           key.route,
			StatusClass:             key.class,
			Count:                   s.count,
			SumDurationNs:           s.sumDurationNs,
			ReqBytes:                s.reqBytes,
			RespBytes:               s.respBytes,
			LatencySketch:           s.sk.Buckets(),
			StatusCodeBreakdown:     s.codes,
			MaxDurationNs:           s.maxDurationNs,
			Exemplars:               s.exemplars(),
			ErrorClassBreakdown:     s.errorClasses,
			NoStatusReasons:         s.noStatusReasons,
			SumDownstreamDurationNs: s.sumDownstreamNs,
		})
	}

	// Attach one per-window resource snapshot (USE gauges) so saturation can be
	// correlated with the RED metrics in the same upload. Sampled here on the
	// uploader goroutine, matching the window the summaries cover.
	instanceWindows := []*mapingv1.InstanceWindow{r.sampler.sample(end.Add(-r.cfg.FlushWindow), end)}

	return &mapingv1.UploadRequest{
		InstanceWindows: instanceWindows,
		Envelope: &mapingv1.Envelope{
			Service:             r.cfg.Service,
			Instance:            r.cfg.Instance,
			SdkVersion:          SdkVersion,
			SketchFormatVersion: sketch.SketchFormatVersion,
			// Deploy identity: per-process constants stamped on every Envelope so
			// the server can store version/env/region as low-cardinality
			// dimensions (not part of the series key).
			DeployVersion:       r.cfg.DeployVersion,
			DeployId:            r.cfg.DeployID,
			Environment:         r.cfg.Environment,
			Region:              r.cfg.Region,
			InstanceStartTimeMs: r.startTime.UnixMilli(),
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
