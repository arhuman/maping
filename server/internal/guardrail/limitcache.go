package guardrail

import (
	"context"
	"sync"
	"time"
)

// Cache TTLs, mirroring control/resolver.go's rationale for key resolution.
// Positive results are cached longer than negatives, but negatives are cached
// too so a missing org — or a down control plane, which the provider chain
// also maps to ok=false — does not hammer Postgres, while still recovering
// fast once the cause clears.
const (
	limitPositiveTTL = 30 * time.Second
	limitNegativeTTL = 5 * time.Second
	// limitCacheSweepThreshold bounds the limits cache the same way
	// control/resolver.go bounds its resolution cache. Entries are never
	// removed on read (the fast path only skips stale ones), so on write, once
	// the map exceeds this size we sweep and delete entries whose expiry is
	// already past. If every entry is still live after the sweep the map may
	// stay above the threshold — that is acceptable (all entries are in use);
	// we deliberately avoid LRU eviction to keep the cache simple and correct.
	// The threshold is generous so a healthy tenant space never triggers a
	// sweep.
	limitCacheSweepThreshold = 4096
)

// limitCacheEntry is a cached limits resolution with its expiry. ok is cached
// alongside the value so negative results ("could not resolve") are memoized
// too, just with a shorter TTL.
type limitCacheEntry struct {
	limits Limits
	ok     bool
	expiry time.Time
}

// CachedLimitProvider is a caching decorator over a LimitProvider. Without it,
// every ingest Upload resolves plan limits 2–3 times — the rate limiter
// re-tunes from provider.Limits on every Allow, and the cardinality and
// payload providers each call it per request — and each resolution is a
// Postgres query. The decorator sits at the END of the provider chain wired in
// app.buildIngestWiring, so ALL three per-request consumers (rate re-tune,
// cardinality cap, payload cap) and any composing build's decorated provider
// resolve from memory; steady-state cost drops to at most one control-plane
// query per tenant per TTL window.
//
// Accepted staleness: a plan change (or a composing build's suspend decision)
// takes effect within limitPositiveTTL. That trade mirrors the key-resolution
// cache in control/resolver.go, which already accepts the same window for
// revocations.
type CachedLimitProvider struct {
	base LimitProvider

	mu    sync.RWMutex
	cache map[string]limitCacheEntry

	// now is the injectable clock for tests; time.Now in production.
	now func() time.Time
}

// NewCachedLimitProvider wraps base with an in-memory TTL cache.
func NewCachedLimitProvider(base LimitProvider) *CachedLimitProvider {
	return &CachedLimitProvider{
		base:  base,
		cache: make(map[string]limitCacheEntry),
		now:   time.Now,
	}
}

// Limits returns the tenant's cached budget when fresh, otherwise resolves
// through the wrapped provider and caches the result — ok=true for
// limitPositiveTTL, ok=false for limitNegativeTTL.
func (c *CachedLimitProvider) Limits(ctx context.Context, tenant string) (Limits, bool) {
	if entry, hit := c.getFresh(tenant); hit {
		return entry.limits, entry.ok
	}

	limits, ok := c.base.Limits(ctx, tenant)

	ttl := limitPositiveTTL
	if !ok {
		ttl = limitNegativeTTL
	}
	c.store(tenant, limitCacheEntry{limits: limits, ok: ok, expiry: c.now().Add(ttl)})
	return limits, ok
}

// getFresh returns a cache entry if present and unexpired.
func (c *CachedLimitProvider) getFresh(tenant string) (limitCacheEntry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.cache[tenant]
	if !ok || !entry.expiry.After(c.now()) {
		return limitCacheEntry{}, false
	}
	return entry, true
}

// store writes a cache entry, sweeping expired entries first when the map has
// grown past limitCacheSweepThreshold so a churning tenant space cannot
// accumulate dead entries without bound. The sweep only removes entries
// already past their expiry, so no live (fresh) resolution is ever evicted —
// the TTL semantics are untouched.
func (c *CachedLimitProvider) store(tenant string, entry limitCacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.cache) >= limitCacheSweepThreshold {
		now := c.now()
		for k, e := range c.cache {
			if !e.expiry.After(now) {
				delete(c.cache, k)
			}
		}
	}
	c.cache[tenant] = entry
}
