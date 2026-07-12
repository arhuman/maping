package ingest

import (
	"sync"

	"golang.org/x/time/rate"
)

// tenantLimiter is a per-tenant in-memory token-bucket rate limiter. It is the
// minimal default guardrail; plan-driven per-tenant limits come from the
// control plane via guardrail.RateLimiter.
type tenantLimiter struct {
	mu       sync.Mutex
	limiters map[string]*rate.Limiter
	rps      rate.Limit
	burst    int
}

// newTenantLimiter builds a limiter that allows rps requests per second per
// tenant with the given burst.
func newTenantLimiter(rps float64, burst int) *tenantLimiter {
	return &tenantLimiter{
		limiters: make(map[string]*rate.Limiter),
		rps:      rate.Limit(rps),
		burst:    burst,
	}
}

// allow reports whether a request for tenant may proceed, consuming a token.
func (t *tenantLimiter) allow(tenant string) bool {
	t.mu.Lock()
	lim, ok := t.limiters[tenant]
	if !ok {
		lim = rate.NewLimiter(t.rps, t.burst)
		t.limiters[tenant] = lim
	}
	t.mu.Unlock()
	return lim.Allow()
}
