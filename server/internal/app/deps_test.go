package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arhuman/maping/server/internal/control"
	"github.com/arhuman/maping/server/internal/guardrail"
	"github.com/arhuman/maping/server/internal/storage"
	"github.com/arhuman/maping/server/internal/tenant"
	"github.com/arhuman/maping/server/internal/web"

	"github.com/arhuman/maping/proto/token"
)

// csrfKey is a 32-byte HMAC key for the control-plane wiring tests (web.NewHandler
// requires a CSRF key whenever a KeyAdmin is present).
var csrfKey = []byte("0123456789abcdef0123456789abcdef")

func TestBuildWebConfigStaticDevMode(t *testing.T) {
	// No control plane (cp nil, card nil, auth off): the dashboard renders the
	// constant dev tenant and every control-plane-dependent field stays nil.
	cfg := buildWebConfig(nopQuerier{}, nil, nil, false, devTenant, "", nil, testLogger())

	assert.Nil(t, cfg.KeyAdmin, "no control plane -> no keys panel")
	assert.Nil(t, cfg.Onboarding, "no control plane -> no onboarding source")
	assert.Nil(t, cfg.Frozen, "no cardinality guard -> no frozen func")

	require.NotNil(t, cfg.Tenant)
	tid, ok := cfg.Tenant(httptest.NewRequest(http.MethodGet, "/", nil))
	assert.True(t, ok)
	assert.Equal(t, devTenant, tid.String())

	_, err := web.NewHandler(cfg)
	require.NoError(t, err)
}

func TestBuildWebConfigWithControlPlane(t *testing.T) {
	cp := &fakeControlPlane{}
	card := guardrail.NewCardinality()
	// Control plane present + auth on: keys panel, onboarding, CSRF and frozen
	// all wired, and the tenant resolver is the auth (session-context) one.
	cfg := buildWebConfig(nopQuerier{}, cp, card, true, devTenant, "https://maping.example.com", csrfKey, testLogger())

	assert.NotNil(t, cfg.KeyAdmin, "control plane -> keys panel present")
	assert.NotNil(t, cfg.Onboarding, "control plane -> onboarding source present")
	assert.NotNil(t, cfg.Frozen, "cardinality guard -> frozen func present")
	assert.Equal(t, csrfKey, cfg.CSRFKey)

	// Auth-on tenant resolver reads the session context: no session -> unresolved
	// (this is the gate that keeps unauthenticated callers out).
	require.NotNil(t, cfg.Tenant)
	_, ok := cfg.Tenant(httptest.NewRequest(http.MethodGet, "/", nil))
	assert.False(t, ok, "auth on + no session -> tenant unresolved (401)")

	_, err := web.NewHandler(cfg)
	require.NoError(t, err)
}

func TestBuildWebConfigOnboardingMapsFields(t *testing.T) {
	ts := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	cp := &fakeControlPlane{onboarding: []control.ServiceOnboarding{
		{Service: "checkout", Instance: "pod-a", HandshakeAt: ts},
	}}
	cfg := buildWebConfig(nopQuerier{}, cp, nil, true, devTenant, "", csrfKey, testLogger())

	got, err := cfg.Onboarding(context.Background(), "tenant")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, web.ServiceOnboarding{Service: "checkout", Instance: "pod-a", HandshakeAt: ts}, got[0])
}

func TestBuildWebConfigOnboardingPropagatesError(t *testing.T) {
	cp := &fakeControlPlane{onboardErr: errors.New("boom")}
	cfg := buildWebConfig(nopQuerier{}, cp, nil, true, devTenant, "", csrfKey, testLogger())
	_, err := cfg.Onboarding(context.Background(), "tenant")
	require.Error(t, err)
}

func TestKeyAdminIssueWrapsOrigin(t *testing.T) {
	cp := &fakeControlPlane{issueSecret: "s3cr3t"}
	ka := keyAdmin{cp: cp, baseURL: "https://collector.example.com"}

	tok, err := ka.IssueKey(context.Background(), "org-1", "checkout-api")
	require.NoError(t, err)
	assert.Equal(t, "checkout-api", cp.issuedLabel, "label passed through to the store")

	// The returned token carries both the deployment origin and the raw secret.
	origin, secret, ok := token.Decode(tok)
	require.True(t, ok)
	assert.Equal(t, "https://collector.example.com", origin)
	assert.Equal(t, "s3cr3t", secret)
}

func TestKeyAdminIssuePropagatesError(t *testing.T) {
	cp := &fakeControlPlane{issueErr: errors.New("insert failed")}
	ka := keyAdmin{cp: cp, baseURL: "https://x"}
	_, err := ka.IssueKey(context.Background(), "org-1", "l")
	require.Error(t, err)
}

func TestKeyAdminListMapsFields(t *testing.T) {
	revoked := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)
	created := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	cp := &fakeControlPlane{keys: []control.KeyInfo{
		{ID: "k1", Label: "active", Last4: "a91f", CreatedAt: created, RevokedAt: nil},
		{ID: "k2", Label: "gone", Last4: "dead", CreatedAt: created, RevokedAt: &revoked},
	}}
	ka := keyAdmin{cp: cp}

	got, err := ka.ListKeys(context.Background(), "org-1")
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, web.KeyInfo{ID: "k1", Label: "active", Last4: "a91f", CreatedAt: created, RevokedAt: nil}, got[0])
	assert.Equal(t, &revoked, got[1].RevokedAt, "revoked timestamp mapped through")
}

func TestKeyAdminListPropagatesError(t *testing.T) {
	cp := &fakeControlPlane{listErr: errors.New("query failed")}
	ka := keyAdmin{cp: cp}
	_, err := ka.ListKeys(context.Background(), "org-1")
	require.Error(t, err)
}

func TestKeyAdminRevokePassthrough(t *testing.T) {
	cp := &fakeControlPlane{}
	ka := keyAdmin{cp: cp}
	require.NoError(t, ka.RevokeKey(context.Background(), "org-1", "k9"))
	assert.Equal(t, "k9", cp.revokedID)
}

func TestBuildAuthNoStore(t *testing.T) {
	// A nil member store (no control plane) yields no auth layer.
	a, err := buildAuth(nil, "https://x", csrfKey, testLogger())
	require.NoError(t, err)
	assert.Nil(t, a, "no store -> no auth layer (constant-tenant mode)")
}

func TestBuildAuthWithStore(t *testing.T) {
	a, err := buildAuth(fakeMemberStore{}, "https://maping.example.com", csrfKey, testLogger())
	require.NoError(t, err)
	require.NotNil(t, a)
	// No OIDC creds configured -> dev-login is the only enabled path.
	providers, devLogin := a.Enabled()
	assert.Empty(t, providers)
	assert.True(t, devLogin)
}

func TestBuildAuthShortKeyErrors(t *testing.T) {
	_, err := buildAuth(fakeMemberStore{}, "https://x", []byte("too-short"), testLogger())
	require.Error(t, err, "auth.New must reject a session key shorter than 32 bytes")
}

// TestScopedQuerier_Tenant verifies that scopedQuerier.Tenant returns a non-nil
// web.ScopedQuery bound to the supplied tenant. It uses a nil ClickHouse
// connection (safe because Tenant() creates the struct only — no network call).
func TestScopedQuerier_Tenant(t *testing.T) {
	qs := storage.NewQueryService(nil) // nil conn is safe: Tenant does not query
	sq := scopedQuerier{qs: qs}

	// Verify it satisfies the web.Querier interface at the type level.
	var _ web.Querier = sq

	// Calling Tenant must return a non-nil web.ScopedQuery.
	scoped := sq.Tenant(tenant.MustParse("tenant-abc"))
	assert.NotNil(t, scoped, "Tenant must return a non-nil ScopedQuery")

	// A second call with a different tenant must return an independent handle.
	scoped2 := sq.Tenant(tenant.MustParse("tenant-xyz"))
	assert.NotNil(t, scoped2)
	assert.NotEqual(t, scoped, scoped2,
		"different tenants must produce distinct ScopedQuery handles")
}
