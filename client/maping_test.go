package maping

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arhuman/maping/client/internal/buffer"
	mapingv1 "github.com/arhuman/maping/proto/maping/v1"
	"github.com/arhuman/maping/proto/token"
)

// fakeTransport is an injectable uploader that records calls (lang-go DI).
type fakeTransport struct {
	mu        sync.Mutex
	registers []*mapingv1.Handshake
	uploads   []*mapingv1.UploadRequest
}

func (f *fakeTransport) Register(_ context.Context, hs *mapingv1.Handshake) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.registers = append(f.registers, hs)
	return nil
}

func (f *fakeTransport) Upload(_ context.Context, req *mapingv1.UploadRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uploads = append(f.uploads, req)
	return nil
}

func (f *fakeTransport) uploadCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.uploads)
}

func TestResolveConfigPrecedence(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		opts    []Option
		wantKey string
		wantEnd string
		wantSvc string
	}{
		{
			name:    "code option beats env",
			env:     map[string]string{"MAPING_KEY": "envkey", "MAPING_ENDPOINT": "https://env"},
			opts:    []Option{WithKey("codekey"), WithEndpoint("https://code")},
			wantKey: "codekey",
			wantEnd: "https://code",
		},
		{
			name:    "env beats default",
			env:     map[string]string{"MAPING_KEY": "envkey", "MAPING_ENDPOINT": "https://env"},
			wantKey: "envkey",
			wantEnd: "https://env",
		},
		{
			name:    "default endpoint when unset",
			env:     map[string]string{"MAPING_KEY": "k"},
			wantKey: "k",
			wantEnd: defaultEndpoint,
		},
		{
			name:    "service from MAPING_SERVICE beats OTEL",
			env:     map[string]string{"MAPING_SERVICE": "checkout", "OTEL_SERVICE_NAME": "otel"},
			wantSvc: "checkout",
		},
		{
			name:    "service from OTEL when MAPING_SERVICE unset",
			env:     map[string]string{"OTEL_SERVICE_NAME": "otel"},
			wantSvc: "otel",
		},
		{
			name:    "key-embedded origin used when no endpoint set",
			env:     map[string]string{"MAPING_KEY": token.Encode("https://collector.example.com", "sec")},
			wantEnd: "https://collector.example.com",
		},
		{
			name:    "MAPING_ENDPOINT beats key-embedded origin",
			env:     map[string]string{"MAPING_KEY": token.Encode("https://collector.example.com", "sec"), "MAPING_ENDPOINT": "https://env"},
			wantEnd: "https://env",
		},
		{
			name:    "WithEndpoint beats key-embedded origin",
			env:     map[string]string{"MAPING_KEY": token.Encode("https://collector.example.com", "sec")},
			opts:    []Option{WithEndpoint("https://code")},
			wantEnd: "https://code",
		},
		{
			name:    "non-http embedded origin falls back to default",
			env:     map[string]string{"MAPING_KEY": token.Encode("file:///etc/passwd", "sec")},
			wantEnd: defaultEndpoint,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearMapingEnv(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			cfg := resolveConfig(tt.opts)
			if tt.wantKey != "" {
				assert.Equal(t, tt.wantKey, cfg.Key)
			}
			if tt.wantEnd != "" {
				assert.Equal(t, tt.wantEnd, cfg.Endpoint)
			}
			if tt.wantSvc != "" {
				assert.Equal(t, tt.wantSvc, cfg.Service)
			}
		})
	}
}

func TestFlushWindowParsing(t *testing.T) {
	tests := []struct {
		name string
		env  string
		opt  time.Duration
		want time.Duration
	}{
		{name: "default when unset", env: "", want: defaultFlushWindow},
		{name: "parsed from env", env: "5", want: 5 * time.Second},
		{name: "invalid falls back to default", env: "abc", want: defaultFlushWindow},
		{name: "non-positive falls back to default", env: "0", want: defaultFlushWindow},
		{name: "code option beats env", env: "5", opt: 3 * time.Second, want: 3 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearMapingEnv(t)
			if tt.env != "" {
				t.Setenv("MAPING_FLUSH_SECONDS", tt.env)
			}
			var opts []Option
			if tt.opt > 0 {
				opts = append(opts, WithFlushWindow(tt.opt))
			}
			cfg := resolveConfig(opts)
			assert.Equal(t, tt.want, cfg.FlushWindow)
		})
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		status int
		want   mapingv1.StatusClass
	}{
		{0, mapingv1.StatusClass_STATUS_CLASS_NO_STATUS},
		{-1, mapingv1.StatusClass_STATUS_CLASS_NO_STATUS},
		{99, mapingv1.StatusClass_STATUS_CLASS_NO_STATUS},
		{200, mapingv1.StatusClass_STATUS_CLASS_2XX},
		{204, mapingv1.StatusClass_STATUS_CLASS_2XX},
		{301, mapingv1.StatusClass_STATUS_CLASS_3XX},
		{404, mapingv1.StatusClass_STATUS_CLASS_4XX},
		{499, mapingv1.StatusClass_STATUS_CLASS_4XX},
		{500, mapingv1.StatusClass_STATUS_CLASS_5XX},
		{503, mapingv1.StatusClass_STATUS_CLASS_5XX},
	}
	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			assert.Equal(t, tt.want, classify(tt.status))
		})
	}
}

func TestNoOpRecorderWhenKeyAbsent(t *testing.T) {
	clearMapingEnv(t)
	before := goroutineFingerprint()

	r := NewRecorder() // no key
	assert.False(t, r.active(), "recorder must be no-op without a key")

	// Observe must be safe and do nothing.
	r.Observe(Record{Method: "GET", RouteTemplate: "/x", Status: 200, Duration: time.Millisecond})
	require.NoError(t, r.Shutdown(context.Background()))

	assert.Equal(t, before, goroutineFingerprint(), "no-op recorder must not spawn a goroutine")
	assert.Nil(t, r.shards, "no-op recorder must not allocate shards")
	assert.Nil(t, r.ring, "no-op recorder must not allocate a ring")
}

// newTestRecorder builds a running-shape recorder wired to fake, without the
// background goroutine, so white-box tests can drive Observe/flush directly.
func newTestRecorder(fake Uploader) *Recorder {
	shards := new([numShards]shard)
	for i := range shards {
		shards[i].m = make(map[seriesKey]*series)
	}
	return &Recorder{
		cfg:    Config{Service: "svc", Instance: "inst", FlushWindow: time.Second},
		tx:     fake,
		shards: shards,
		ring:   buffer.New(ringCapacity(time.Second)),
	}
}

// collectSeries merges every shard's series into one map for assertions,
// mirroring what flush does but without consuming the shards.
func (r *Recorder) collectSeries() map[seriesKey]*series {
	out := make(map[seriesKey]*series)
	for i := range r.shards {
		sh := &r.shards[i]
		sh.mu.Lock()
		for k, s := range sh.m {
			out[k] = s
		}
		sh.mu.Unlock()
	}
	return out
}

func TestObserveAggregatesIntoSeries(t *testing.T) {
	r := newTestRecorder(&fakeTransport{})

	r.Observe(Record{Method: "GET", RouteTemplate: "/users/:id", Status: 200, Duration: 10 * time.Millisecond, ReqBytes: 100, RespBytes: 200})
	r.Observe(Record{Method: "GET", RouteTemplate: "/users/:id", Status: 201, Duration: 20 * time.Millisecond, ReqBytes: 50, RespBytes: 300})
	r.Observe(Record{Method: "GET", RouteTemplate: "/users/:id", Status: 500, Duration: 5 * time.Millisecond})

	// 200 and 201 share the 2XX series; 500 is its own series.
	key2xx := seriesKey{method: "GET", route: "/users/:id", class: mapingv1.StatusClass_STATUS_CLASS_2XX}
	key5xx := seriesKey{method: "GET", route: "/users/:id", class: mapingv1.StatusClass_STATUS_CLASS_5XX}

	all := r.collectSeries()
	require.Len(t, all, 2)
	s := all[key2xx]
	require.NotNil(t, s)
	assert.Equal(t, uint64(2), s.count)
	assert.Equal(t, uint64((10+20)*time.Millisecond.Nanoseconds()), s.sumDurationNs)
	assert.Equal(t, uint64(150), s.reqBytes)
	assert.Equal(t, uint64(500), s.respBytes)
	assert.Equal(t, uint64(2), s.sk.Count())
	assert.Equal(t, uint64(1), s.codes[200])
	assert.Equal(t, uint64(1), s.codes[201])

	assert.Equal(t, uint64(1), all[key5xx].count)
}

func TestRecordCodeBounded(t *testing.T) {
	codes := make(map[uint32]uint64)
	for i := range maxStatusCodes + 10 {
		recordCode(codes, 200+i)
	}
	assert.Len(t, codes, maxStatusCodes, "distinct codes must be bounded")
	// An already-present code past the cap still increments.
	recordCode(codes, 200)
	assert.Equal(t, uint64(2), codes[200])
}

func TestFlushBuildsRequestAndSwapsShards(t *testing.T) {
	fake := &fakeTransport{}
	r := newTestRecorder(fake)
	r.Observe(Record{Method: "POST", RouteTemplate: "/o", Status: 200, Duration: time.Millisecond, ReqBytes: 10, RespBytes: 20})

	// flush pushes the built request onto the ring (fail-open: it does not send).
	require.True(t, r.flush())
	require.Equal(t, 1, r.ring.Len(), "flush must enqueue one pending upload")
	assert.Equal(t, 0, fake.uploadCount(), "flush must not upload synchronously")

	// Draining the ring sends the request through the transport.
	require.Equal(t, drainSent, r.tryDrainOldest(context.Background()))
	require.Equal(t, 1, fake.uploadCount())
	req := fake.uploads[0]
	assert.Equal(t, "svc", req.Envelope.Service)
	assert.Equal(t, "inst", req.Envelope.Instance)
	assert.Equal(t, SdkVersion, req.Envelope.SdkVersion)
	require.Len(t, req.Summaries, 1)
	sum := req.Summaries[0]
	assert.Equal(t, "POST", sum.Method)
	assert.Equal(t, "/o", sum.RouteTemplate)
	assert.Equal(t, mapingv1.StatusClass_STATUS_CLASS_2XX, sum.StatusClass)
	assert.Equal(t, uint64(1), sum.Count)
	assert.NotEmpty(t, sum.LatencySketch)

	// Shards swapped: an empty flush enqueues nothing more.
	assert.False(t, r.flush(), "empty window must not enqueue")
	assert.Equal(t, 0, r.ring.Len())
}

func TestRecorderLifecycleWithFakeTransport(t *testing.T) {
	fake := &fakeTransport{}
	cfg := Config{Service: "svc", Instance: "inst", FlushWindow: 50 * time.Millisecond}
	r := newRecorderWithTransport(cfg, fake)

	r.Observe(Record{Method: "GET", RouteTemplate: "/x", Status: 200, Duration: time.Millisecond})

	// Accelerated first flush (~2s) is long; drive shutdown for a deterministic
	// final flush instead of waiting.
	require.NoError(t, r.Shutdown(context.Background()))

	assert.Len(t, fake.registers, 1, "startup handshake must be sent")
	assert.GreaterOrEqual(t, fake.uploadCount(), 1, "final flush must upload buffered data")
}

// flakyTransport fails Upload until failUntil successful attempts remain, then
// succeeds — to exercise retry/backoff and dropped accounting.
type flakyTransport struct {
	mu        sync.Mutex
	failCount int // number of leading Upload calls that return an error
	uploads   []*mapingv1.UploadRequest
}

func (f *flakyTransport) Register(context.Context, *mapingv1.Handshake) error { return nil }

func (f *flakyTransport) Upload(_ context.Context, req *mapingv1.UploadRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failCount > 0 {
		f.failCount--
		return assert.AnError
	}
	f.uploads = append(f.uploads, req)
	return nil
}

func TestFlushPushesToRingFailOpen(t *testing.T) {
	// A failing transport must not lose data at flush time: the request stays in
	// the ring, and a later successful drain ships it.
	fake := &flakyTransport{failCount: 2}
	r := newTestRecorder(fake)
	r.Observe(Record{Method: "GET", RouteTemplate: "/x", Status: 200, Duration: time.Millisecond})

	require.True(t, r.flush())
	assert.Equal(t, drainFailed, r.tryDrainOldest(context.Background()))
	assert.Equal(t, drainFailed, r.tryDrainOldest(context.Background()))
	assert.Equal(t, 1, r.ring.Len(), "failed sends leave the request pending")

	assert.Equal(t, drainSent, r.tryDrainOldest(context.Background()))
	assert.Equal(t, 0, r.ring.Len())
	assert.Len(t, fake.uploads, 1, "the pending request is eventually delivered")
}

func TestDroppedAccountingStampedAtSendTime(t *testing.T) {
	// A tiny ring forces drop-oldest; the dropped count must surface on the next
	// successful envelope, then reset.
	fake := &fakeTransport{}
	r := newTestRecorder(fake)
	r.ring = buffer.New(1) // capacity 1: every extra push drops the oldest

	// Push three windows; the first two are dropped (1 summary each).
	for range 3 {
		r.Observe(Record{Method: "GET", RouteTemplate: "/x", Status: 200, Duration: time.Millisecond})
		require.True(t, r.flush())
	}
	assert.Equal(t, uint64(2), r.droppedSummaries, "two evicted requests, one summary each")
	require.Equal(t, 1, r.ring.Len())

	require.Equal(t, drainSent, r.tryDrainOldest(context.Background()))
	require.Len(t, fake.uploads, 1)
	assert.Equal(t, uint64(2), fake.uploads[0].Envelope.DroppedSummaries, "gap reported on the next successful envelope")
	assert.Equal(t, uint64(0), r.droppedSummaries, "counter resets after a successful upload")
}

func TestObserveRecoversPanic(t *testing.T) {
	// A nil shards pointer would panic inside Observe; the recovery must swallow
	// it so the host request is never affected.
	r := &Recorder{
		cfg:    Config{Service: "svc", FlushWindow: time.Second},
		tx:     &fakeTransport{},
		shards: nil, // deliberately broken to force a panic in the body
		ring:   buffer.New(8),
	}
	assert.NotPanics(t, func() {
		r.Observe(Record{Method: "GET", RouteTemplate: "/x", Status: 200})
	}, "an internal panic must be invisible to the host")
}

func TestShutdownDrainsRing(t *testing.T) {
	fake := &fakeTransport{}
	cfg := Config{Service: "svc", Instance: "inst", FlushWindow: 50 * time.Millisecond}
	r := newRecorderWithTransport(cfg, fake)

	r.Observe(Record{Method: "GET", RouteTemplate: "/x", Status: 200, Duration: time.Millisecond})
	require.NoError(t, r.Shutdown(context.Background()))

	assert.GreaterOrEqual(t, fake.uploadCount(), 1, "buffered summaries must be shipped on graceful shutdown")
	assert.Equal(t, 0, r.ring.Len(), "ring must be fully drained")
}

func TestNextBackoffCaps(t *testing.T) {
	tests := []struct {
		cur, want time.Duration
	}{
		{baseBackoff, 2 * baseBackoff},
		{16 * time.Second, maxBackoff},
		{maxBackoff, maxBackoff},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, nextBackoff(tt.cur))
	}
}

// clearMapingEnv unsets all mAPI-ng-relevant env vars for the duration of a test.
func clearMapingEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"MAPING_KEY", "MAPING_ENDPOINT", "MAPING_SERVICE",
		"MAPING_INSTANCE", "MAPING_FLUSH_SECONDS", "OTEL_SERVICE_NAME", "HOSTNAME",
	} {
		t.Setenv(k, "")
	}
}

// goroutineFingerprint returns the current goroutine count, used to assert a
// no-op recorder starts no goroutine.
func goroutineFingerprint() int {
	return runtime.NumGoroutine()
}

// TestNewRecorderForTest verifies the seam constructor returns a running,
// active recorder bound to the injected uploader.
func TestNewRecorderForTest(t *testing.T) {
	fake := &fakeTransport{}
	r := NewRecorderForTest(fake)
	require.NotNil(t, r)
	assert.True(t, r.active(), "NewRecorderForTest must return an active recorder")
	assert.NotNil(t, r.shards, "shards must be allocated")
	assert.NotNil(t, r.ring, "ring must be allocated")
	// Observe must work without panic.
	r.Observe(Record{Method: "GET", RouteTemplate: "/test", Status: 200, Duration: time.Millisecond})
	// Shutdown must drain without error.
	require.NoError(t, r.Shutdown(context.Background()))
}

// TestNewRecorderNoOpWhenKeyEmpty verifies NewRecorder returns a no-op recorder
// when no ingest key can be resolved (CONFIG.md fail-open).
func TestNewRecorderNoOpWhenKeyEmpty(t *testing.T) {
	clearMapingEnv(t)
	r := NewRecorder() // no key in env, no option
	assert.False(t, r.active())
	assert.Nil(t, r.shards)
	assert.Nil(t, r.ring)
}

// TestNewRecorderNoOpOnBadEndpoint verifies NewRecorder logs once and returns
// a no-op recorder when the endpoint scheme is unusable — the host is never
// affected.
func TestNewRecorderNoOpOnBadEndpoint(t *testing.T) {
	clearMapingEnv(t)
	// A key forces the real-transport branch; ftp:// is rejected by transport.New.
	r := NewRecorder(WithKey("k"), WithEndpoint("ftp://bad"))
	assert.False(t, r.active(), "bad endpoint must yield a no-op recorder")
}

// TestNewRecorderWithValidKey verifies NewRecorder returns an active recorder
// and can be shut down cleanly when a key and valid endpoint are present.
func TestNewRecorderWithValidKey(t *testing.T) {
	clearMapingEnv(t)
	// Use a syntactically valid https:// endpoint — no live server needed
	// because Shutdown stops the goroutine before any upload is attempted.
	r := NewRecorder(WithKey("test-key"), WithEndpoint("https://localhost:9999"))
	require.True(t, r.active(), "valid key+endpoint must produce an active recorder")
	require.NoError(t, r.Shutdown(context.Background()))
}

// TestResetTimer verifies resetTimer safely resets a timer regardless of
// whether the previous tick was already consumed or not.
func TestResetTimer(t *testing.T) {
	// Case 1: reset a timer whose C has a pending value (already fired).
	fired := time.NewTimer(0)
	time.Sleep(time.Millisecond) // let it fire
	resetTimer(fired, 10*time.Millisecond)
	select {
	case <-fired.C:
		// good
	case <-time.After(100 * time.Millisecond):
		t.Fatal("resetTimer: timer did not fire after reset from fired state")
	}

	// Case 2: reset a timer that has NOT fired yet.
	notFired := time.NewTimer(time.Hour)
	resetTimer(notFired, 10*time.Millisecond)
	select {
	case <-notFired.C:
		// good
	case <-time.After(100 * time.Millisecond):
		t.Fatal("resetTimer: timer did not fire after reset from active state")
	}
}

// TestRunStopChannel verifies that Shutdown closes the stop channel which
// causes run() to exit, making the done channel observable as closed.
func TestRunStopChannel(t *testing.T) {
	fake := &fakeTransport{}
	cfg := Config{Service: "svc", Instance: "inst", FlushWindow: 10 * time.Second}
	r := newRecorderWithTransport(cfg, fake)
	require.NoError(t, r.Shutdown(context.Background()))
	// After Shutdown the done channel must be closed.
	select {
	case <-r.done:
		// good: goroutine exited
	default:
		t.Fatal("done channel must be closed after Shutdown")
	}
}

// TestShutdownContextExpired verifies Shutdown returns ctx.Err() when context
// expires while the ring still has pending uploads that cannot be drained.
// This exercises the ctx.Err() branch of Shutdown's drain loop.
func TestShutdownContextExpired(t *testing.T) {
	// A never-succeeding transport keeps the ring non-empty after every drain
	// attempt. Use newRecorderWithTransport (which starts the goroutine and
	// initialises stop/done) so Shutdown can close the stop channel safely.
	never := &flakyTransport{failCount: 1<<31 - 1}
	cfg := Config{Service: "svc", Instance: "inst", FlushWindow: time.Second}
	r := newRecorderWithTransport(cfg, never)
	r.Observe(Record{Method: "GET", RouteTemplate: "/x", Status: 200, Duration: time.Millisecond})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	err := r.Shutdown(ctx)
	assert.Error(t, err, "Shutdown must return ctx error when drain times out")
}
