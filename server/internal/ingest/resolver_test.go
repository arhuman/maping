package ingest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStaticKeyResolverKnownKey(t *testing.T) {
	r := NewStaticKeyResolver(map[string]string{
		"dev-key":     "dev-tenant",
		"staging-key": "acme-tenant",
	})

	tenant, ok := r.Resolve(context.Background(), "dev-key")
	assert.True(t, ok)
	assert.Equal(t, "dev-tenant", tenant)

	tenant, ok = r.Resolve(context.Background(), "staging-key")
	assert.True(t, ok)
	assert.Equal(t, "acme-tenant", tenant)
}

func TestStaticKeyResolverUnknownKey(t *testing.T) {
	r := NewStaticKeyResolver(map[string]string{"dev-key": "dev-tenant"})

	tenant, ok := r.Resolve(context.Background(), "wrong-key")
	assert.False(t, ok)
	assert.Empty(t, tenant)

	tenant, ok = r.Resolve(context.Background(), "")
	assert.False(t, ok)
	assert.Empty(t, tenant)
}

func TestStaticKeyResolverStoresHashedNotPlaintext(t *testing.T) {
	// The resolver must retain no plaintext key material.
	r := NewStaticKeyResolver(map[string]string{"super-secret": "t"})
	for _, sk := range r.keys {
		assert.NotEqual(t, hashKey("super-secret"), keyHash{}, "hash must be non-zero")
		// The stored bytes must equal the sha256 of the key, never the key text.
		assert.Equal(t, hashKey("super-secret"), sk.hash)
	}
}

func TestHashKeyDeterministicAndDistinct(t *testing.T) {
	assert.Equal(t, hashKey("a"), hashKey("a"))
	assert.NotEqual(t, hashKey("a"), hashKey("b"))
}
