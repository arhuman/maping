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
//
// A cached limiter is re-tuned to the currently resolved budget on every call, so
// a plan change (upgrade/downgrade) or a suspension takes
// effect immediately without a process restart. In particular a suspended tenant
// resolves to MaxRPS:0/Burst:0, which SetLimit/SetBurst turn into a hard deny —
// the enforcement lever the control plane relies on. SetLimit/SetBurst preserve
// the bucket's accumulated tokens, so re-tuning to an unchanged budget is a no-op.
func (r *RateLimiter) Allow(ctx context.Context, tenant string) bool {
	limits := DefaultLimits()
	if r.provider != nil {
		if l, ok := r.provider.Limits(ctx, tenant); ok {
			limits = l
		}
	}
	rps := rate.Limit(limits.MaxRPS)

	r.mu.Lock()
	lim, ok := r.limiters[tenant]
	if !ok {
		lim = rate.NewLimiter(rps, limits.Burst)
		r.limiters[tenant] = lim
	} else {
		if lim.Limit() != rps {
			lim.SetLimit(rps)
		}
		if lim.Burst() != limits.Burst {
			lim.SetBurst(limits.Burst)
		}
	}
	r.mu.Unlock()

	return lim.Allow()
}
