//go:build integration

// Package control integration tests exercise the control-plane store CRUD
// (key issuance/revocation/listing, member upsert, handshake recording, plan
// limits) against a live Postgres instance. They are SKIPPED in the normal
// `go test` run and only compile under the `integration` build tag.
//
// Run them with a live Postgres:
//
//	# start a dev stack or use: docker run --rm -e POSTGRES_USER=maping \
//	#   -e POSTGRES_PASSWORD=maping -e POSTGRES_DB=maping -p 5432:5432 postgres:17
//	go test -tags=integration -race ./internal/control/...
//
// The DSN comes from MAPING_POSTGRES_DSN, defaulting to the dev/CI instance.
// New() applies the embedded migrations automatically, so no manual schema step
// is needed. Each test truncates the relevant tables for isolation.
package control

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

const defaultPostgresDSN = "postgres://maping:maping@localhost:5432/maping?sslmode=disable"

func postgresDSN() string {
	if v := os.Getenv("MAPING_POSTGRES_DSN"); v != "" {
		return v
	}
	return defaultPostgresDSN
}

// mustStore opens a Store against the live Postgres, applying migrations. The
// store is closed automatically via t.Cleanup.
func mustStore(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	s, err := New(ctx, postgresDSN())
	require.NoError(t, err, "control.New: check MAPING_POSTGRES_DSN and that Postgres is running")
	t.Cleanup(s.Close)
	return s
}

// truncateTables wipes the tables touched by the integration tests so each
// test starts clean. CASCADE handles FK dependents automatically.
func truncateTables(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	// Order matters: children before parents for non-cascade clarity, though
	// CASCADE on org deletion would cover them.
	tables := []string{"handshakes", "ingest_keys", "members", "orgs"}
	for _, tbl := range tables {
		_, err := s.pool.Exec(ctx, "TRUNCATE TABLE "+tbl+" CASCADE")
		require.NoError(t, err, "truncate %s", tbl)
	}
}

// ── Key lifecycle ─────────────────────────────────────────────────────────────

// TestEnsureDevKeyIntegration verifies that EnsureDevKey is idempotent and
// returns a stable tenant id across repeated calls with the same key.
func TestEnsureDevKeyIntegration(t *testing.T) {
	s := mustStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	const (
		devKey  = "integration-dev-key"
		orgName = "itest-devorg"
	)

	tenant1, err := s.EnsureDevKey(ctx, devKey, orgName)
	require.NoError(t, err)
	require.NotEmpty(t, tenant1)

	// Idempotent: second call with the same key must return the same tenant.
	tenant2, err := s.EnsureDevKey(ctx, devKey, orgName)
	require.NoError(t, err)
	require.Equal(t, tenant1, tenant2, "EnsureDevKey must be idempotent")

	// Same org name, different key → same org, new key row (re-uses existing org).
	tenant3, err := s.EnsureDevKey(ctx, "other-dev-key", orgName)
	require.NoError(t, err)
	require.Equal(t, tenant1, tenant3, "second key under same org must return same tenant")
}

// TestIssueKeyIntegration exercises the full key lifecycle:
// IssueKey → ListKeys (active) → Resolve (via poolLookup) → RevokeKey →
// ListKeys (revoked) → Resolve after revocation.
func TestIssueKeyIntegration(t *testing.T) {
	s := mustStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	// Seed an org to issue keys into.
	var orgID string
	err := s.pool.QueryRow(ctx,
		`INSERT INTO orgs (name) VALUES ('itest-org') RETURNING id::text`,
	).Scan(&orgID)
	require.NoError(t, err)

	// Issue a key and get its plaintext secret back (only once).
	secret, err := s.IssueKey(ctx, orgID, "test-label")
	require.NoError(t, err)
	require.NotEmpty(t, secret)

	// The secret must have a last-4 fragment stored, and ListKeys shows it.
	keys, err := s.ListKeys(ctx, orgID)
	require.NoError(t, err)
	require.Len(t, keys, 1)
	k := keys[0]
	require.Equal(t, "test-label", k.Label)
	require.Equal(t, secret[len(secret)-4:], k.Last4, "Last4 must be the trailing 4 chars of the secret")
	require.Nil(t, k.RevokedAt, "new key must be active (RevokedAt nil)")
	require.NotEmpty(t, k.ID)

	// The key resolves to the correct tenant via poolLookup (the real DB lookup
	// backing Resolver.Resolve). We test poolLookup directly to avoid the
	// token.Encode wrapper and cache layer — integration scope is the DB path.
	lookup := poolLookup(s.pool)
	tenant, ok, err := lookup(ctx, hashKey(secret))
	require.NoError(t, err)
	require.True(t, ok, "active key must resolve")
	require.Equal(t, orgID, tenant)

	// RevokeKey: the key should no longer be active.
	err = s.RevokeKey(ctx, orgID, k.ID)
	require.NoError(t, err)

	// ListKeys must now show the key as revoked.
	keys, err = s.ListKeys(ctx, orgID)
	require.NoError(t, err)
	require.Len(t, keys, 1)
	require.NotNil(t, keys[0].RevokedAt, "revoked key must have non-nil RevokedAt")

	// A revoked key must no longer resolve.
	tenant, ok, err = lookup(ctx, hashKey(secret))
	require.NoError(t, err)
	require.False(t, ok, "revoked key must not resolve")
	require.Empty(t, tenant)
}

// TestRevokeKeyUnknownIntegration verifies that revoking a non-existent key
// (wrong org, already revoked, unknown id) returns ErrKeyNotFound.
func TestRevokeKeyUnknownIntegration(t *testing.T) {
	s := mustStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	// Seed org and issue a key so the table is non-empty.
	var orgID string
	require.NoError(t, s.pool.QueryRow(ctx,
		`INSERT INTO orgs (name) VALUES ('itest-revoke-org') RETURNING id::text`,
	).Scan(&orgID))

	_, err := s.IssueKey(ctx, orgID, "active")
	require.NoError(t, err)

	// Revoke with a bogus key id.
	err = s.RevokeKey(ctx, orgID, "00000000-0000-0000-0000-000000000000")
	require.True(t, errors.Is(err, ErrKeyNotFound), "wrong id must return ErrKeyNotFound, got %v", err)

	// Wrong org for a real key id (issue key, then try wrong org).
	secret2, err := s.IssueKey(ctx, orgID, "second")
	require.NoError(t, err)
	keys, err := s.ListKeys(ctx, orgID)
	require.NoError(t, err)
	var keyID string
	for _, k := range keys {
		if k.Last4 == secret2[len(secret2)-4:] {
			keyID = k.ID
		}
	}
	require.NotEmpty(t, keyID)

	// Different org — must not match.
	var otherOrgID string
	require.NoError(t, s.pool.QueryRow(ctx,
		`INSERT INTO orgs (name) VALUES ('itest-other-org') RETURNING id::text`,
	).Scan(&otherOrgID))

	err = s.RevokeKey(ctx, otherOrgID, keyID)
	require.True(t, errors.Is(err, ErrKeyNotFound), "wrong org must return ErrKeyNotFound, got %v", err)

	// Double-revoke: revoke the real key, then try again.
	require.NoError(t, s.RevokeKey(ctx, orgID, keyID))
	err = s.RevokeKey(ctx, orgID, keyID)
	require.True(t, errors.Is(err, ErrKeyNotFound), "double-revoke must return ErrKeyNotFound, got %v", err)
}

// TestListKeysOrderIntegration verifies ListKeys returns keys newest-first and
// includes both active and revoked keys.
func TestListKeysOrderIntegration(t *testing.T) {
	s := mustStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	var orgID string
	require.NoError(t, s.pool.QueryRow(ctx,
		`INSERT INTO orgs (name) VALUES ('itest-list-org') RETURNING id::text`,
	).Scan(&orgID))

	// Issue two keys; the second one is revoked.
	secret1, err := s.IssueKey(ctx, orgID, "first")
	require.NoError(t, err)
	secret2, err := s.IssueKey(ctx, orgID, "second")
	require.NoError(t, err)

	keys, err := s.ListKeys(ctx, orgID)
	require.NoError(t, err)
	require.Len(t, keys, 2)

	// Newest first: the second-issued key should appear first.
	require.Equal(t, secret2[len(secret2)-4:], keys[0].Last4, "newest key first")
	require.Equal(t, secret1[len(secret1)-4:], keys[1].Last4)

	// Revoke the first key and re-list: both still appear, first now revoked.
	require.NoError(t, s.RevokeKey(ctx, orgID, keys[1].ID))
	keys, err = s.ListKeys(ctx, orgID)
	require.NoError(t, err)
	require.Len(t, keys, 2, "revoked key must still appear in list")
	// The revoked key is the older one (index 1).
	require.NotNil(t, keys[1].RevokedAt, "older key must be revoked")
	require.Nil(t, keys[0].RevokedAt, "newer key still active")
}

// ── Plan limits ───────────────────────────────────────────────────────────────

// TestLimitsIntegration verifies that Limits returns the seeded free-plan row
// for a known tenant and falls back to DefaultLimits for an unknown tenant.
func TestLimitsIntegration(t *testing.T) {
	s := mustStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	// Seed an org on the free plan (plan_limits row is seeded by migrations).
	var orgID string
	require.NoError(t, s.pool.QueryRow(ctx,
		`INSERT INTO orgs (name, plan) VALUES ('itest-limits-org', 'free') RETURNING id::text`,
	).Scan(&orgID))

	limits, err := s.Limits(ctx, orgID)
	require.NoError(t, err)
	require.Equal(t, 100.0, limits.MaxRPS)
	require.Equal(t, 200, limits.Burst)
	require.Equal(t, 10000, limits.CardinalityCap)
	require.Equal(t, int64(4194304), limits.MaxPayloadBytes)
	require.Equal(t, 30, limits.RetentionDays)

	// Unknown tenant: must fall back to DefaultLimits without error.
	limits, err = s.Limits(ctx, "00000000-0000-0000-0000-000000000000")
	require.NoError(t, err)
	require.Equal(t, 100.0, limits.MaxRPS, "unknown tenant falls back to DefaultLimits")
}

// ── Member upsert ─────────────────────────────────────────────────────────────

// TestUpsertMemberFromOIDCIntegration verifies first-login creates an org-of-one
// and admin member, and that repeat logins return the same ids without creating
// new rows.
func TestUpsertMemberFromOIDCIntegration(t *testing.T) {
	s := mustStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	const (
		sub   = "github:99999"
		email = "itest@example.com"
	)

	orgID, memberID, role, isNew, err := s.UpsertMemberFromOIDC(ctx, sub, email)
	require.NoError(t, err)
	require.True(t, isNew, "first login must report isNew=true")
	require.NotEmpty(t, orgID)
	require.NotEmpty(t, memberID)
	require.Equal(t, "admin", role)

	// Repeat login: same ids, isNew=false.
	orgID2, memberID2, role2, isNew2, err := s.UpsertMemberFromOIDC(ctx, sub, email)
	require.NoError(t, err)
	require.False(t, isNew2, "repeat login must report isNew=false")
	require.Equal(t, orgID, orgID2)
	require.Equal(t, memberID, memberID2)
	require.Equal(t, role, role2)

	// A different OIDC subject gets a new org-of-one.
	orgID3, _, _, isNew3, err := s.UpsertMemberFromOIDC(ctx, "github:88888", "other@example.com")
	require.NoError(t, err)
	require.True(t, isNew3)
	require.NotEqual(t, orgID, orgID3, "different subject must create a separate org")
}

// ── Handshake recording ───────────────────────────────────────────────────────

// TestRecordHandshakeIntegration verifies RecordHandshake is idempotent (upsert)
// and that OnboardingState returns the recorded services ordered by first_seen.
func TestRecordHandshakeIntegration(t *testing.T) {
	s := mustStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	// Seed a tenant.
	var orgID string
	require.NoError(t, s.pool.QueryRow(ctx,
		`INSERT INTO orgs (name) VALUES ('itest-hs-org') RETURNING id::text`,
	).Scan(&orgID))

	// No handshakes yet: OnboardingState returns empty slice.
	state, err := s.OnboardingState(ctx, orgID)
	require.NoError(t, err)
	require.Empty(t, state)

	// Record a handshake for svc-a.
	require.NoError(t, s.RecordHandshake(ctx, orgID, "svc-a", "inst-1", "v1.0"))

	state, err = s.OnboardingState(ctx, orgID)
	require.NoError(t, err)
	require.Len(t, state, 1)
	require.Equal(t, "svc-a", state[0].Service)
	require.Equal(t, "inst-1", state[0].Instance)
	require.False(t, state[0].HandshakeAt.IsZero())

	// Idempotent: re-handshake must not create a second row.
	require.NoError(t, s.RecordHandshake(ctx, orgID, "svc-a", "inst-1", "v1.1"))
	state, err = s.OnboardingState(ctx, orgID)
	require.NoError(t, err)
	require.Len(t, state, 1, "upsert must not accumulate duplicate rows")

	// A second service appears as a second entry.
	require.NoError(t, s.RecordHandshake(ctx, orgID, "svc-b", "inst-2", "v1.0"))
	state, err = s.OnboardingState(ctx, orgID)
	require.NoError(t, err)
	require.Len(t, state, 2)
	require.Equal(t, "svc-a", state[0].Service, "ordered by first_seen: svc-a was first")
	require.Equal(t, "svc-b", state[1].Service)
}

// TestDevOrgAdminIntegration verifies DevOrgAdmin creates a dev admin member
// on first call and returns the same ids on repeat calls (idempotent).
func TestDevOrgAdminIntegration(t *testing.T) {
	s := mustStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	const devOrg = "itest-dev-org"

	// EnsureDevKey creates the dev org; DevOrgAdmin then seeds an admin member.
	_, err := s.EnsureDevKey(ctx, "dev-admin-key", devOrg)
	require.NoError(t, err)

	orgID, memberID, err := s.DevOrgAdmin(ctx, devOrg)
	require.NoError(t, err)
	require.NotEmpty(t, orgID)
	require.NotEmpty(t, memberID)

	// Idempotent.
	orgID2, memberID2, err := s.DevOrgAdmin(ctx, devOrg)
	require.NoError(t, err)
	require.Equal(t, orgID, orgID2)
	require.Equal(t, memberID, memberID2)

	// Missing org: must return an error.
	_, _, err = s.DevOrgAdmin(ctx, "no-such-org")
	require.Error(t, err, "DevOrgAdmin with missing org must error")
}
