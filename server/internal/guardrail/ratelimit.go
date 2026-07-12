package guardrail

import (
	"context"
	"sync"

	"golang.org/x/time/rate"
)

// LimitProvider resolves a tenant's Limits. The control plane implements it
// (plan_limits lookup, cached); when no provider is wired the RateLimiter falls
// back to DefaultLimits, preserving zero-dependency local dev.
type LimitProvider interface {
	// Limits returns the tenant's budget and ok=false when it cannot be
	// resolved (unknown tenant or provider error).
	Limits(ctx context.Context, tenant string) (Limits, bool)
}

// RateLimiter is a per-tenant token-bucket rate limiter whose budget comes from
// a LimitProvider (the control plane's plan_limits). It generalizes the ingest
// package's hardcoded tenantLimiter with plan-driven limits. When no
// provider is set, or the provider cannot resolve a tenant, it applies
// DefaultLimits so the ingest path never fails open on a missing plan.
type RateLimiter struct {
	provider LimitProvider

	mu       sync.Mutex
	limiters map[string]*rate.Limiter
}

// NewRateLimiter builds a RateLimiter. A nil provider means "always use
// DefaultLimits", which reproduces the fixed 100rps/200burst default.
func NewRateLimiter(provider LimitProvider) *RateLimiter {
	return &RateLimiter{
		provider: provider,
		limiters: make(map[string]*rate.Limiter),
	}
}

// Allow reports whether a request for tenant may proceed, consuming a token.
// The tenant's rps/burst come from the provider (falling back to defaults), and
// the per-tenant limiter is created lazily on first use.
func (r *RateLimiter) Allow(ctx context.Context, tenant string) bool {
	limits := DefaultLimits()
	if r.provider != nil {
		if l, ok := r.provider.Limits(ctx, tenant); ok {
			limits = l
		}
	}

	r.mu.Lock()
	lim, ok := r.limiters[tenant]
	if !ok {
		lim = rate.NewLimiter(rate.Limit(limits.MaxRPS), limits.Burst)
		r.limiters[tenant] = lim
	}
	r.mu.Unlock()

	return lim.Allow()
}
