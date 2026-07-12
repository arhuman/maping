package control

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/arhuman/maping/proto/token"
)

// countingLookup records calls and returns scripted results, so tests can
// assert cache hits (call count stays flat) independently of the DB.
type countingLookup struct {
	mu     sync.Mutex
	calls  int
	tenant string
	ok     bool
	err    error
}

func (c *countingLookup) fn(_ context.Context, _ []byte) (string, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return c.tenant, c.ok, c.err
}

func (c *countingLookup) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func newTestResolver(l *countingLookup, now func() time.Time) *Resolver {
	r := newResolver(l.fn, slog.New(slog.NewTextHandler(errWriter{}, nil)))
	r.now = now
	return r
}

// errWriter discards log output.
type errWriter struct{}

func (errWriter) Write(p []byte) (int, error) { return len(p), nil }

// captureLookup records the last keyHash it was asked to resolve.
type captureLookup struct {
	got    []byte
	called bool
}

func (c *captureLookup) fn(_ context.Context, h []byte) (string, bool, error) {
	c.called = true
	c.got = append([]byte(nil), h...)
	return "org-1", true, nil
}

func TestResolverHashesOnlySecret(t *testing.T) {
	log := slog.New(slog.NewTextHandler(errWriter{}, nil))

	// Structured token: only the secret half is hashed, not the origin.
	structured := &captureLookup{}
	r := newResolver(structured.fn, log)
	r.Resolve(context.Background(), token.Encode("https://collector.example.com", "topsecret"))
	if want := hashKey("topsecret"); !bytes.Equal(structured.got, want) {
		t.Fatalf("structured key hashed %x, want %x (secret only)", structured.got, want)
	}

	// Legacy bare key: the whole value is the secret (keeps dev-key resolving).
	legacy := &captureLookup{}
	r2 := newResolver(legacy.fn, log)
	r2.Resolve(context.Background(), "dev-key")
	if want := hashKey("dev-key"); !bytes.Equal(legacy.got, want) {
		t.Fatalf("legacy key hashed %x, want %x", legacy.got, want)
	}

	// Empty/malformed key is rejected without ever hitting the lookup.
	rejected := &captureLookup{}
	r3 := newResolver(rejected.fn, log)
	if _, ok := r3.Resolve(context.Background(), ""); ok || rejected.called {
		t.Fatalf("empty key must be rejected without a lookup (ok=%v called=%v)", ok, rejected.called)
	}
}

func TestResolverPositiveCacheHit(t *testing.T) {
	base := time.Unix(1_000_000, 0)
	clock := base
	l := &countingLookup{tenant: "org-1", ok: true}
	r := newTestResolver(l, func() time.Time { return clock })

	for i := 0; i < 5; i++ {
		tenant, ok := r.Resolve(context.Background(), "the-key")
		if !ok || tenant != "org-1" {
			t.Fatalf("call %d: got (%q,%v), want (org-1,true)", i, tenant, ok)
		}
	}
	if l.count() != 1 {
		t.Errorf("expected 1 DB lookup with positive cache, got %d", l.count())
	}
}

func TestResolverPositiveCacheExpiry(t *testing.T) {
	base := time.Unix(1_000_000, 0)
	clock := base
	l := &countingLookup{tenant: "org-1", ok: true}
	r := newTestResolver(l, func() time.Time { return clock })

	r.Resolve(context.Background(), "k")
	clock = base.Add(positiveTTL + time.Second) // expire
	r.Resolve(context.Background(), "k")

	if l.count() != 2 {
		t.Errorf("expected re-lookup after TTL expiry, got %d lookups", l.count())
	}
}

func TestResolverNegativeCache(t *testing.T) {
	base := time.Unix(1_000_000, 0)
	clock := base
	l := &countingLookup{ok: false}
	r := newTestResolver(l, func() time.Time { return clock })

	// Two rapid lookups of an unknown key -> one DB call (negative-cached).
	if _, ok := r.Resolve(context.Background(), "bad"); ok {
		t.Fatal("unknown key should resolve ok=false")
	}
	r.Resolve(context.Background(), "bad")
	if l.count() != 1 {
		t.Errorf("negative cache should suppress the second lookup, got %d", l.count())
	}

	// After the shorter negative TTL, it re-checks.
	clock = base.Add(negativeTTL + time.Second)
	r.Resolve(context.Background(), "bad")
	if l.count() != 2 {
		t.Errorf("expected re-lookup after negative TTL, got %d", l.count())
	}
}

func TestResolverFailsClosedOnDBError(t *testing.T) {
	base := time.Unix(1_000_000, 0)
	clock := base
	l := &countingLookup{tenant: "org-1", ok: true, err: errors.New("pg down")}
	r := newTestResolver(l, func() time.Time { return clock })

	tenant, ok := r.Resolve(context.Background(), "k")
	if ok || tenant != "" {
		t.Errorf("DB error with no cache must fail closed, got (%q,%v)", tenant, ok)
	}

	// Nothing cached on error, so a subsequent success is honored.
	l.err = nil
	tenant, ok = r.Resolve(context.Background(), "k")
	if !ok || tenant != "org-1" {
		t.Errorf("after error clears, resolve should succeed, got (%q,%v)", tenant, ok)
	}
}

// distinctLookup returns a distinct tenant per key hash so each Resolve caches a
// new entry, letting the test grow the cache past the sweep threshold.
type distinctLookup struct{}

func (distinctLookup) fn(_ context.Context, keyHash []byte) (string, bool, error) {
	return "org-" + string(keyHash), true, nil
}

// TestResolverCacheSweepEvictsExpired proves that once the cache exceeds the
// sweep threshold, a write removes entries whose expiry is already past the
// injected clock, so a churning key space cannot accumulate dead
// entries without bound.
func TestResolverCacheSweepEvictsExpired(t *testing.T) {
	base := time.Unix(1_000_000, 0)
	clock := base
	r := newResolver(distinctLookup{}.fn, slog.New(slog.NewTextHandler(errWriter{}, nil)))
	r.now = func() time.Time { return clock }

	// Fill the cache to exactly the sweep threshold with entries that will expire.
	for i := 0; i < cacheSweepThreshold; i++ {
		r.Resolve(context.Background(), keyN(i))
	}
	r.mu.RLock()
	filled := len(r.cache)
	r.mu.RUnlock()
	if filled != cacheSweepThreshold {
		t.Fatalf("expected %d cached entries, got %d", cacheSweepThreshold, filled)
	}

	// Advance past the positive TTL so every existing entry is now expired, then
	// write one more entry: the store sweep should drop all expired entries,
	// leaving only the freshly written one.
	clock = base.Add(positiveTTL + time.Second)
	r.Resolve(context.Background(), keyN(cacheSweepThreshold))

	r.mu.RLock()
	remaining := len(r.cache)
	r.mu.RUnlock()
	if remaining != 1 {
		t.Errorf("expected sweep to leave only the fresh entry, got %d", remaining)
	}
}

// keyN builds a distinct key string for cache-growth tests.
func keyN(n int) string {
	return "key-" + strconv.Itoa(n)
}

// TestPoolLookup covers the real key-resolution SQL result handling against a
// fake querier: an active key resolves, a revoked/unknown key (no rows) is a
// clean rejection, and a genuine DB error is propagated so Resolve fails closed.
func TestPoolLookup(t *testing.T) {
	ctx := context.Background()

	found := poolLookup(&scriptedQuerier{rows: []fakeRow{{values: []any{"org-7"}}}})
	tenant, ok, err := found(ctx, []byte("hash"))
	if err != nil || !ok || tenant != "org-7" {
		t.Fatalf("active key: got (%q,%v,%v), want (org-7,true,nil)", tenant, ok, err)
	}

	missing := poolLookup(&scriptedQuerier{rows: []fakeRow{{err: pgx.ErrNoRows}}})
	tenant, ok, err = missing(ctx, []byte("hash"))
	if err != nil || ok || tenant != "" {
		t.Fatalf("revoked/unknown key: got (%q,%v,%v), want (\"\",false,nil)", tenant, ok, err)
	}

	dbErr := errors.New("connection reset")
	failing := poolLookup(&scriptedQuerier{rows: []fakeRow{{err: dbErr}}})
	if _, ok, err := failing(ctx, []byte("hash")); ok || !errors.Is(err, dbErr) {
		t.Fatalf("db error: got (ok=%v, err=%v), want (false, %v)", ok, err, dbErr)
	}
}

// TestResolverLogRateLimited proves the fail-closed error log is throttled to at
// most one line per dbErrorLogEvery window, so a Postgres outage under a stream
// of requests does not flood host logs.
func TestResolverLogRateLimited(t *testing.T) {
	base := time.Unix(1_000_000, 0)
	clock := base
	var buf bytes.Buffer
	l := &countingLookup{err: errors.New("pg down")}
	r := newResolver(l.fn, slog.New(slog.NewTextHandler(&buf, nil)))
	r.now = func() time.Time { return clock }

	// Two rapid failures within the window (nothing is cached on error, so each
	// Resolve re-hits the lookup and re-enters logDBError) -> logged once.
	r.Resolve(context.Background(), "k")
	r.Resolve(context.Background(), "k")
	if got := strings.Count(buf.String(), "fail-closed"); got != 1 {
		t.Errorf("rate-limited error log: got %d lines within the window, want 1", got)
	}

	// After the window elapses, the next failure logs again.
	clock = base.Add(dbErrorLogEvery + time.Second)
	r.Resolve(context.Background(), "k")
	if got := strings.Count(buf.String(), "fail-closed"); got != 2 {
		t.Errorf("after the window, expected a second error log, got %d", got)
	}
}

// TestNewResolverDefaultsLogger covers the nil-logger fallback so a caller that
// passes no logger still gets a working resolver rather than a nil-deref.
func TestNewResolverDefaultsLogger(t *testing.T) {
	r := newResolver((&countingLookup{ok: true, tenant: "x"}).fn, nil)
	if r.log == nil {
		t.Fatal("newResolver(nil logger) must default to slog.Default()")
	}
	// And it still resolves.
	if _, ok := r.Resolve(context.Background(), "k"); !ok {
		t.Error("resolver with defaulted logger must still resolve a valid key")
	}
}

func TestResolverServesFreshCacheDuringOutage(t *testing.T) {
	base := time.Unix(1_000_000, 0)
	clock := base
	l := &countingLookup{tenant: "org-1", ok: true}
	r := newTestResolver(l, func() time.Time { return clock })

	r.Resolve(context.Background(), "k") // populate positive cache

	// DB now failing, but the cache entry is still fresh: keep serving it.
	l.err = errors.New("pg blip")
	tenant, ok := r.Resolve(context.Background(), "k")
	if !ok || tenant != "org-1" {
		t.Errorf("fresh cache should be served during outage, got (%q,%v)", tenant, ok)
	}
	if l.count() != 1 {
		t.Errorf("fresh cache hit must not touch the DB, got %d lookups", l.count())
	}
}
