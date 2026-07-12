// Package ingest is the mAPI-ng collector: the Connect/gRPC IngestService
// handler that authenticates ingest keys, resolves the tenant, enforces the
// timestamp policy, and writes Summary rows to the data plane. Auth and the
// tenant column exist from day one so the model does not churn when the real
// control plane lands.
package ingest

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
)

// KeyResolver maps an ingest key to a tenant. The control-plane implementation
// queries Postgres; local dev and tests use StaticKeyResolver.
type KeyResolver interface {
	// Resolve returns the tenant for key and ok=false if the key is unknown.
	Resolve(ctx context.Context, key string) (tenant string, ok bool)
}

// keyHash is the sha256 digest of an ingest key. Keys are stored hashed, never
// in plaintext.
type keyHash [sha256.Size]byte

// hashKey computes the sha256 digest of an ingest key.
func hashKey(key string) keyHash {
	return sha256.Sum256([]byte(key))
}

// staticKey pairs a stored key digest with its tenant.
type staticKey struct {
	hash   keyHash
	tenant string
}

// StaticKeyResolver is an in-memory KeyResolver for local dev and tests. Keys
// are stored hashed and every candidate is compared with constant-time equality
// so neither a match nor a mismatch leaks key material through timing.
type StaticKeyResolver struct {
	keys []staticKey
}

// NewStaticKeyResolver builds a resolver from a plaintext key -> tenant map,
// hashing each key at construction so no plaintext is retained.
func NewStaticKeyResolver(keyToTenant map[string]string) *StaticKeyResolver {
	keys := make([]staticKey, 0, len(keyToTenant))
	for key, tenant := range keyToTenant {
		keys = append(keys, staticKey{hash: hashKey(key), tenant: tenant})
	}
	return &StaticKeyResolver{keys: keys}
}

// Resolve hashes the presented key and compares it against every stored digest
// with constant-time equality. The loop does not short-circuit on the first
// match, so its timing does not depend on which (or whether a) key matched.
func (r *StaticKeyResolver) Resolve(_ context.Context, key string) (string, bool) {
	h := hashKey(key)
	var (
		tenant string
		found  bool
	)
	for _, sk := range r.keys {
		if subtle.ConstantTimeCompare(h[:], sk.hash[:]) == 1 {
			tenant = sk.tenant
			found = true
		}
	}
	return tenant, found
}
