package guardrail

import (
	"context"
	"testing"
)

// fakeProvider serves fixed limits per tenant and reports unknown tenants.
type fakeProvider struct {
	limits map[string]Limits
}

func (f fakeProvider) Limits(_ context.Context, tenant string) (Limits, bool) {
	l, ok := f.limits[tenant]
	return l, ok
}

func TestRateLimiterUsesProviderBudget(t *testing.T) {
	ctx := context.Background()
	provider := fakeProvider{limits: map[string]Limits{
		"tight": {MaxRPS: 1, Burst: 1},
	}}
	rl := NewRateLimiter(provider)

	if !rl.Allow(ctx, "tight") {
		t.Fatalf("first request should be allowed by the burst-1 bucket")
	}
	if rl.Allow(ctx, "tight") {
		t.Errorf("second immediate request should be throttled by burst-1 bucket")
	}
}

func TestRateLimiterFallsBackToDefaults(t *testing.T) {
	ctx := context.Background()

	// Nil provider -> DefaultLimits (burst 200): many rapid calls pass.
	rl := NewRateLimiter(nil)
	for i := 0; i < DefaultLimits().Burst; i++ {
		if !rl.Allow(ctx, "t") {
			t.Fatalf("request %d should be allowed under default burst", i)
		}
	}

	// Unknown tenant from a provider also falls back to defaults.
	rlp := NewRateLimiter(fakeProvider{limits: map[string]Limits{}})
	if !rlp.Allow(ctx, "unknown") {
		t.Errorf("unknown tenant should fall back to default budget, not be blocked")
	}
}

// TestRateLimiterSuspensionHardDenies proves that flipping a previously-active
// tenant's budget to MaxRPS:0/Burst:0 (the control plane's suspension lever) takes
// effect on the already-cached limiter: the very next request is denied.
func TestRateLimiterSuspensionHardDenies(t *testing.T) {
	ctx := context.Background()
	provider := fakeProvider{limits: map[string]Limits{
		"t": {MaxRPS: 100, Burst: 10},
	}}
	rl := NewRateLimiter(provider)

	if !rl.Allow(ctx, "t") {
		t.Fatalf("active tenant's first request should be allowed")
	}
	// Suspend: a zeroed budget (as a composing provider returns for a suspended
	// tenant) must hard-deny the next request.
	provider.limits["t"] = Limits{MaxRPS: 0, Burst: 0}
	if rl.Allow(ctx, "t") {
		t.Errorf("suspended tenant (MaxRPS:0/Burst:0) must be hard-denied on the next request")
	}
}

// TestRateLimiterDowngradeTightensBurst proves a downgrade to a smaller burst
// re-tunes the cached limiter: a tenant that was allowed a wide burst is capped to
// the new, tighter budget rather than staying pinned to the first-seen one.
func TestRateLimiterDowngradeTightensBurst(t *testing.T) {
	ctx := context.Background()
	provider := fakeProvider{limits: map[string]Limits{
		"t": {MaxRPS: 1, Burst: 5},
	}}
	rl := NewRateLimiter(provider)

	// Prime the cached limiter under the generous budget with a single call
	// (tokens remain near the burst-5 cap).
	if !rl.Allow(ctx, "t") {
		t.Fatalf("first request under burst-5 should be allowed")
	}
	// Downgrade to burst-1/rate-1: SetBurst caps the outstanding tokens to 1, so at
	// most one more immediate request is served before the bucket is dry.
	provider.limits["t"] = Limits{MaxRPS: 1, Burst: 1}
	served := 0
	for i := 0; i < 5; i++ {
		if rl.Allow(ctx, "t") {
			served++
		}
	}
	if served > 1 {
		t.Errorf("downgraded burst-1 budget must cap the cached limiter, served %d immediate requests", served)
	}
}

func TestRateLimiterTenantIsolation(t *testing.T) {
	ctx := context.Background()
	provider := fakeProvider{limits: map[string]Limits{
		"a": {MaxRPS: 1, Burst: 1},
		"b": {MaxRPS: 1, Burst: 1},
	}}
	rl := NewRateLimiter(provider)

	if !rl.Allow(ctx, "a") || !rl.Allow(ctx, "b") {
		t.Fatalf("first request per tenant should be allowed independently")
	}
	if rl.Allow(ctx, "a") {
		t.Errorf("tenant a should be throttled after consuming its burst")
	}
}
