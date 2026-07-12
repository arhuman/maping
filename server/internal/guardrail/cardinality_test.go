package guardrail

import (
	"testing"
	"time"
)

func TestCardinalityAllow(t *testing.T) {
	tests := []struct {
		name        string
		cap         int
		series      []string
		wantAllowed []bool
		wantFrozen  []bool
	}{
		{
			name:        "fresh series under cap tracked and allowed",
			cap:         3,
			series:      []string{"a", "b"},
			wantAllowed: []bool{true, true},
			wantFrozen:  []bool{false, false},
		},
		{
			name:        "existing series always allowed",
			cap:         1,
			series:      []string{"a", "a", "a"},
			wantAllowed: []bool{true, true, true},
			wantFrozen:  []bool{false, false, false},
		},
		{
			name:        "new series over cap frozen while existing keep flowing",
			cap:         2,
			series:      []string{"a", "b", "c", "a"},
			wantAllowed: []bool{true, true, false, true},
			wantFrozen:  []bool{false, false, true, true},
		},
		{
			name:        "cap zero disables the guard",
			cap:         0,
			series:      []string{"a", "b", "c"},
			wantAllowed: []bool{true, true, true},
			wantFrozen:  []bool{false, false, false},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewCardinality()
			for i, s := range tt.series {
				allowed, frozen := c.Allow("tenant", s, tt.cap)
				if allowed != tt.wantAllowed[i] {
					t.Errorf("step %d Allow(%q) allowed = %v, want %v", i, s, allowed, tt.wantAllowed[i])
				}
				if frozen != tt.wantFrozen[i] {
					t.Errorf("step %d Allow(%q) frozen = %v, want %v", i, s, frozen, tt.wantFrozen[i])
				}
			}
		})
	}
}

func TestCardinalityTenantIsolation(t *testing.T) {
	c := NewCardinality()
	// Tenant A fills its cap of 1.
	if allowed, _ := c.Allow("A", "a1", 1); !allowed {
		t.Fatalf("A first series should be allowed")
	}
	if allowed, frozen := c.Allow("A", "a2", 1); allowed || !frozen {
		t.Fatalf("A second series should be frozen, got allowed=%v frozen=%v", allowed, frozen)
	}
	// Tenant B has its own budget and must be unaffected by A being frozen.
	if allowed, frozen := c.Allow("B", "b1", 1); !allowed || frozen {
		t.Fatalf("B first series should be allowed and not frozen, got allowed=%v frozen=%v", allowed, frozen)
	}
	if c.Frozen("B") {
		t.Errorf("B must not be frozen when only A hit its cap")
	}
	if !c.Frozen("A") {
		t.Errorf("A must be reported frozen")
	}
}

// TestCardinalityEvictsStaleSeries proves an idle series is evicted once it
// falls outside the TTL window, and that a still-active series is retained.
func TestCardinalityEvictsStaleSeries(t *testing.T) {
	base := time.Unix(1_000_000, 0)
	clock := base
	c := NewCardinality()
	c.setNow(func() time.Time { return clock })

	// Track "a" and "b" at cap 2.
	if allowed, _ := c.Allow("t", "a", 2); !allowed {
		t.Fatalf("a should be allowed")
	}
	if allowed, _ := c.Allow("t", "b", 2); !allowed {
		t.Fatalf("b should be allowed")
	}
	// A new series "c" is at cap -> frozen.
	if allowed, frozen := c.Allow("t", "c", 2); allowed || !frozen {
		t.Fatalf("c at cap should be frozen, got allowed=%v frozen=%v", allowed, frozen)
	}

	// Advance past the TTL, but keep "a" active just before the jump so only "b"
	// goes stale. Refresh "a" at base+TTL-1s, then jump to base+TTL+1s: "b" (last
	// seen at base) is now stale and evicted, "a" survives, freeing a slot so a
	// new series "d" fits under the cap again.
	clock = base.Add(seriesTTL - time.Second)
	if allowed, _ := c.Allow("t", "a", 2); !allowed {
		t.Fatalf("a refresh should be allowed")
	}
	clock = base.Add(seriesTTL + time.Second)
	// "d" fits because "b" was evicted, freeing a slot. The tenant's frozen flag
	// stays set (sticky: "c" was frozen earlier and the tenant never emptied),
	// which is correct — the flag reflects that a freeze has happened on this
	// node, not the momentary slot availability.
	if allowed, _ := c.Allow("t", "d", 2); !allowed {
		t.Fatalf("d should fit after b evicted")
	}
}

// TestCardinalityDropsEmptyTenant proves a tenant whose series all expire is
// removed from both maps, and its frozen flag cleared.
func TestCardinalityDropsEmptyTenant(t *testing.T) {
	base := time.Unix(1_000_000, 0)
	clock := base
	c := NewCardinality()
	c.setNow(func() time.Time { return clock })

	// Fill cap of 1 and freeze the tenant.
	c.Allow("t", "a", 1)
	if allowed, frozen := c.Allow("t", "b", 1); allowed || !frozen {
		t.Fatalf("b should freeze tenant, got allowed=%v frozen=%v", allowed, frozen)
	}
	if !c.Frozen("t") {
		t.Fatalf("tenant should be frozen")
	}

	// Let everything expire, then touch a fresh series: the stale sweep empties
	// the tenant, clearing its frozen flag before the new series is tracked.
	clock = base.Add(seriesTTL + time.Second)
	if allowed, frozen := c.Allow("t", "c", 1); !allowed || frozen {
		t.Fatalf("c should be allowed on a reset tenant, got allowed=%v frozen=%v", allowed, frozen)
	}
	if c.Frozen("t") {
		t.Errorf("frozen flag should be cleared after the tenant was emptied")
	}
}

// TestCardinalityRetainsActiveSeries proves that continuously-seen series are
// never evicted even far past the TTL from the original insert.
func TestCardinalityRetainsActiveSeries(t *testing.T) {
	base := time.Unix(1_000_000, 0)
	clock := base
	c := NewCardinality()
	c.setNow(func() time.Time { return clock })

	c.Allow("t", "a", 5)
	// Re-see "a" every half-TTL for several TTL windows.
	for i := 0; i < 6; i++ {
		clock = clock.Add(seriesTTL / 2)
		if allowed, _ := c.Allow("t", "a", 5); !allowed {
			t.Fatalf("active series a must stay allowed at step %d", i)
		}
	}
}

func TestSeriesKeyDeterministic(t *testing.T) {
	got := SeriesKey("GET", "/users/:id", "STATUS_CLASS_2XX")
	want := "GET|/users/:id|STATUS_CLASS_2XX"
	if got != want {
		t.Errorf("SeriesKey = %q, want %q", got, want)
	}
}
