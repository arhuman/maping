package guardrail

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingProvider is a fake base provider that counts invocations and serves
// an injectable result. The counter is atomic so the concurrency test can run
// under -race.
type countingProvider struct {
	calls  atomic.Int64
	limits Limits
	ok     bool
}

func (p *countingProvider) Limits(context.Context, string) (Limits, bool) {
	p.calls.Add(1)
	return p.limits, p.ok
}

// newTestCache builds a CachedLimitProvider over base with a manually advanced
// clock, returning the cache and the clock-advance function.
func newTestCache(base LimitProvider) (*CachedLimitProvider, func(d time.Duration)) {
	now := time.Now()
	var mu sync.Mutex
	c := NewCachedLimitProvider(base)
	c.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	advance := func(d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(d)
	}
	return c, advance
}

func TestCachedLimitProviderHitCaches(t *testing.T) {
	ctx := context.Background()
	base := &countingProvider{limits: Limits{MaxRPS: 42, Burst: 7}, ok: true}
	c, _ := newTestCache(base)

	l1, ok1 := c.Limits(ctx, "t")
	l2, ok2 := c.Limits(ctx, "t")

	if got := base.calls.Load(); got != 1 {
		t.Errorf("base should be invoked once for two cached calls, got %d", got)
	}
	if !ok1 || !ok2 {
		t.Errorf("both calls should report ok, got %v and %v", ok1, ok2)
	}
	if l1 != l2 || l1.MaxRPS != 42 {
		t.Errorf("cached result should match base result, got %+v then %+v", l1, l2)
	}
}

func TestCachedLimitProviderPositiveExpiry(t *testing.T) {
	ctx := context.Background()
	base := &countingProvider{limits: Limits{MaxRPS: 1}, ok: true}
	c, advance := newTestCache(base)

	c.Limits(ctx, "t")
	advance(limitPositiveTTL + time.Second)
	c.Limits(ctx, "t")

	if got := base.calls.Load(); got != 2 {
		t.Errorf("base should be re-invoked after the positive TTL, got %d calls", got)
	}
}

func TestCachedLimitProviderNegativeCaching(t *testing.T) {
	ctx := context.Background()
	base := &countingProvider{ok: false}
	c, advance := newTestCache(base)

	// Within the negative TTL the miss is served from cache.
	if _, ok := c.Limits(ctx, "t"); ok {
		t.Fatalf("base returns ok=false, cache must not report ok")
	}
	if _, ok := c.Limits(ctx, "t"); ok {
		t.Fatalf("cached negative must stay ok=false")
	}
	if got := base.calls.Load(); got != 1 {
		t.Errorf("negative result should be cached within limitNegativeTTL, got %d calls", got)
	}

	// A negative entry must NOT live the positive TTL: advancing past the
	// negative TTL but well before the positive one re-fetches.
	advance(limitNegativeTTL + time.Second)
	c.Limits(ctx, "t")
	if got := base.calls.Load(); got != 2 {
		t.Errorf("base should be re-invoked once the negative TTL passed, got %d calls", got)
	}
}

func TestCachedLimitProviderSweepShrinksMap(t *testing.T) {
	ctx := context.Background()
	base := &countingProvider{ok: true}
	c, advance := newTestCache(base)

	// Fill past the sweep threshold, then expire everything.
	for i := 0; i <= limitCacheSweepThreshold; i++ {
		c.Limits(ctx, fmt.Sprintf("tenant-%d", i))
	}
	advance(limitPositiveTTL + time.Second)

	c.mu.RLock()
	before := len(c.cache)
	c.mu.RUnlock()
	if before <= limitCacheSweepThreshold {
		t.Fatalf("setup: cache should exceed the sweep threshold, has %d entries", before)
	}

	// One more write triggers the sweep of the now-expired entries.
	c.Limits(ctx, "fresh")
	c.mu.RLock()
	after := len(c.cache)
	c.mu.RUnlock()
	if after >= before {
		t.Errorf("write over threshold should sweep expired entries: %d -> %d", before, after)
	}
	if after != 1 {
		t.Errorf("only the fresh entry should survive the sweep, got %d entries", after)
	}
}

func TestCachedLimitProviderConcurrentAccess(t *testing.T) {
	ctx := context.Background()
	base := &countingProvider{limits: Limits{MaxRPS: 5}, ok: true}
	c, _ := newTestCache(base)

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			tenant := fmt.Sprintf("tenant-%d", n%4)
			for j := 0; j < 100; j++ {
				if l, ok := c.Limits(ctx, tenant); !ok || l.MaxRPS != 5 {
					t.Errorf("concurrent read got (%+v, %v), want cached base result", l, ok)
					return
				}
			}
		}(i)
	}
	wg.Wait()
}
