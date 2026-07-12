package guardrail

import (
	"sync"
	"time"
)

// seriesTTL bounds how long an idle series stays tracked before eviction, which
// bounds the per-node maps. It matches the dashboard's live
// window (1h): a series not seen within the TTL is treated as no longer part of
// the active working set, so on the next Allow sweep it is evicted and its slot
// in the tenant's cardinality budget is freed. This keeps the per-node cap a
// sliding window over the ACTIVE set rather than an ever-growing lifetime count,
// which is the correct best-effort semantic for a long-running node under series
// churn: an idle series releasing budget is intended, not a leak.
const seriesTTL = time.Hour

// Cardinality is a BEST-EFFORT, PER-NODE series-cardinality guard. It tracks the
// set of distinct series each tenant has produced on THIS node and, once that
// set reaches the tenant's cap, freezes NEW series while letting already-tracked
// ones through (CONTEXT: "freeze new series, keep existing"). It is deliberately
// NOT a globally consistent registry: a true cross-node "freeze exactly the new
// ones" needs distributed series-key sync and violates the simplicity pillar.
// Each node freezes independently, so the effective cap is per-node, not
// per-tenant-cluster-wide. The dashboard surfaces the
// per-tenant frozen state exposed by Frozen.
//
// The series key is method|route_template|status_class. Instance is excluded so
// the same endpoint across replicas counts once (matches the CONTEXT series-key
// intent for a per-node budget); it is kept deterministic for testability.
//
// Memory is bounded by lazy TTL eviction: each tracked series
// carries a last-seen timestamp, and Allow sweeps a tenant's stale entries
// (older than seriesTTL) before counting, so the tracked set reflects the active
// working set. An emptied tenant is dropped entirely (tracked + frozenAt) so a
// churning key space cannot grow the maps without bound.
type Cardinality struct {
	mu       sync.Mutex
	tracked  map[string]map[string]time.Time // tenant -> series key -> last seen
	frozenAt map[string]bool                 // tenant -> has frozen at least one new series
	now      func() time.Time                // injectable clock for deterministic tests
}

// NewCardinality builds an empty cardinality guard using the wall clock.
func NewCardinality() *Cardinality {
	return &Cardinality{
		tracked:  make(map[string]map[string]time.Time),
		frozenAt: make(map[string]bool),
		now:      time.Now,
	}
}

// setNow overrides the clock for deterministic eviction tests. Unexported: it is
// a test seam only, mirroring the resolver's injected now.
func (c *Cardinality) setNow(now func() time.Time) {
	c.now = now
}

// evictStale drops from set every series whose last-seen is older than seriesTTL
// relative to now. Caller holds c.mu. Returns the number of survivors so the
// caller can drop an emptied tenant.
func evictStale(set map[string]time.Time, now time.Time) int {
	for key, seen := range set {
		if now.Sub(seen) >= seriesTTL {
			delete(set, key)
		}
	}
	return len(set)
}

// SeriesKey builds the deterministic per-node series key from the series
// dimensions that matter for cardinality (instance excluded).
func SeriesKey(method, routeTemplate, statusClass string) string {
	return method + "|" + routeTemplate + "|" + statusClass
}

// Allow reports whether a series may be ingested for tenant under cap, and
// whether the tenant is currently in a frozen state. Semantics:
//   - already-tracked series -> allowed (existing series keep flowing);
//   - untracked series with tracked count < cap -> tracked and allowed;
//   - untracked series at/over cap -> rejected (frozen), tenant marked frozen.
//
// A cap <= 0 disables the guard (always allow), so a missing/zero plan value
// never accidentally blocks ingest.
func (c *Cardinality) Allow(tenant, seriesKey string, capacity int) (allowed, frozen bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()

	set, ok := c.tracked[tenant]
	if !ok {
		set = make(map[string]time.Time)
		c.tracked[tenant] = set
	}

	// Sweep this tenant's stale series before counting so the cap reflects the
	// active working set. If the sweep empties the set, drop the
	// tenant entirely (tracked + frozenAt) so an idle/churning tenant leaves no
	// residue in either map; the current series is re-tracked below.
	if survivors := evictStale(set, now); survivors == 0 {
		delete(c.tracked, tenant)
		delete(c.frozenAt, tenant)
		set = make(map[string]time.Time)
		c.tracked[tenant] = set
	}

	if _, tracked := set[seriesKey]; tracked {
		// Existing series keeps flowing; refresh its last-seen so it stays in the
		// active window.
		set[seriesKey] = now
		return true, c.frozenAt[tenant]
	}

	if capacity <= 0 || len(set) < capacity {
		set[seriesKey] = now
		return true, c.frozenAt[tenant]
	}

	// At cap: freeze this new series, remember the tenant is frozen.
	c.frozenAt[tenant] = true
	return false, true
}

// Frozen reports whether tenant has had at least one new series frozen on this
// node. Exposed for dashboard surfacing; read-only.
func (c *Cardinality) Frozen(tenant string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.frozenAt[tenant]
}
