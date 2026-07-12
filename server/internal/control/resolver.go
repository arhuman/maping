package control

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arhuman/maping/proto/token"
)

// Cache TTLs. Positive results are cached longer than negatives, but negatives
// are cached too so a stream of bad keys does not hammer Postgres.
const (
	positiveTTL = 30 * time.Second
	negativeTTL = 5 * time.Second
	// dbErrorLogEvery rate-limits the fail-closed error log so a Postgres
	// outage does not flood host logs.
	dbErrorLogEvery = 5 * time.Second
	// cacheSweepThreshold bounds the resolution cache to keep the per-node maps
	// from growing without bound. Entries are never removed on read (getFresh only skips
	// them), so on write, once the map exceeds this size we sweep and delete
	// entries whose expiry is already past. If every entry is still live after
	// the sweep the map may stay above the threshold — that is acceptable (all
	// entries are in use); we deliberately avoid LRU eviction to keep the cache
	// simple and correct. The threshold is generous so a healthy key space never
	// triggers a sweep.
	cacheSweepThreshold = 4096
)

// lookupFunc runs the parameterized key -> tenant lookup. It returns
// (tenant, true, nil) on an active key, (_, false, nil) for an unknown/revoked
// key, and a non-nil error only for an actual database failure. It is a field
// (not a hardcoded call) so the resolver's cache logic is unit-testable with a
// fake lookup and no live Postgres.
type lookupFunc func(ctx context.Context, keyHash []byte) (tenant string, ok bool, err error)

// cacheEntry is a cached resolution with its expiry.
type cacheEntry struct {
	tenant string
	ok     bool
	expiry time.Time
}

// Resolver resolves an ingest key to a tenant against the control plane, with an
// in-memory TTL cache. It structurally satisfies ingest.KeyResolver (the
// error-free Resolve(ctx, key) (string, bool)) so main can pass it in without
// ingest importing control.
//
// Fail-closed policy: on a database error with no usable cache entry, Resolve
// returns ok=false (rejects the key) and logs loudly but rate-limited. Auth
// safety is chosen over availability here: a Postgres blip must not let unknown
// keys through. A still-fresh cache entry is honored during the outage.
type Resolver struct {
	lookup lookupFunc
	log    *slog.Logger

	mu    sync.RWMutex
	cache map[string]cacheEntry // keyed by the sha256 digest (as string)

	logMu       sync.Mutex
	lastErrorAt time.Time

	now func() time.Time
}

// NewResolver builds a Resolver backed by a control-plane pool. The lookup is
// the real parameterized query; the cache is empty.
func NewResolver(pool *pgxpool.Pool, log *slog.Logger) *Resolver {
	return newResolver(poolLookup(pool), log)
}

// newResolver is the injectable constructor used by tests to supply a fake
// lookup.
func newResolver(lookup lookupFunc, log *slog.Logger) *Resolver {
	if log == nil {
		log = slog.Default()
	}
	return &Resolver{
		lookup: lookup,
		log:    log,
		cache:  make(map[string]cacheEntry),
		now:    time.Now,
	}
}

// poolLookup builds the real database lookup: an active (non-revoked) key
// resolves to its org id, which is the tenant. It takes the querier interface
// (satisfied by *pgxpool.Pool) so the SQL-result handling — active key found,
// revoked/unknown key (no rows), and a genuine DB error — is unit-testable
// against a fake without a live Postgres.
func poolLookup(q querier) lookupFunc {
	return func(ctx context.Context, keyHash []byte) (string, bool, error) {
		var tenant string
		err := q.QueryRow(ctx,
			`SELECT org_id::text FROM ingest_keys WHERE key_hash = $1 AND revoked_at IS NULL`,
			keyHash,
		).Scan(&tenant)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		if err != nil {
			return "", false, err
		}
		return tenant, true, nil
	}
}

// Resolve returns the tenant for key, hitting the cache first and falling back
// to the control-plane lookup. See the type doc for the fail-closed policy.
func (r *Resolver) Resolve(ctx context.Context, key string) (string, bool) {
	// Hash only the secret half: the key may carry an embedded collector origin
	// (mk_live_<origin>.<secret>) that is client-only routing metadata and never
	// part of the credential. A legacy bare key decodes to itself as the secret.
	_, secret, ok := token.Decode(key)
	if !ok {
		return "", false
	}
	digest := string(hashKey(secret))

	if entry, hit := r.getFresh(digest); hit {
		return entry.tenant, entry.ok
	}

	tenant, ok, err := r.lookup(ctx, []byte(digest))
	if err != nil {
		// Fail closed. Honor a stale-but-present entry if any (already handled
		// above only for fresh); with no fresh entry we reject and log.
		r.logDBError(err)
		return "", false
	}

	ttl := positiveTTL
	if !ok {
		ttl = negativeTTL
	}
	r.store(digest, cacheEntry{tenant: tenant, ok: ok, expiry: r.now().Add(ttl)})
	return tenant, ok
}

// getFresh returns a cache entry if present and unexpired.
func (r *Resolver) getFresh(digest string) (cacheEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.cache[digest]
	if !ok || !entry.expiry.After(r.now()) {
		return cacheEntry{}, false
	}
	return entry, true
}

// store writes a cache entry, sweeping expired entries first when the map has
// grown past cacheSweepThreshold so a churning key space cannot accumulate dead
// entries without bound. The sweep only removes entries already
// past their expiry, so no live (fresh) resolution is ever evicted — the
// fail-closed policy and TTL semantics are untouched.
func (r *Resolver) store(digest string, entry cacheEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.cache) >= cacheSweepThreshold {
		now := r.now()
		for k, e := range r.cache {
			if !e.expiry.After(now) {
				delete(r.cache, k)
			}
		}
	}
	r.cache[digest] = entry
}

// logDBError logs the fail-closed database error at most once per
// dbErrorLogEvery so an outage does not flood logs.
func (r *Resolver) logDBError(err error) {
	r.logMu.Lock()
	defer r.logMu.Unlock()
	now := r.now()
	if now.Sub(r.lastErrorAt) < dbErrorLogEvery {
		return
	}
	r.lastErrorAt = now
	r.log.Error("control: key resolution failed, rejecting key (fail-closed)", slog.Any("err", err))
}
