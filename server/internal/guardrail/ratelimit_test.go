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
