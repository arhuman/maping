package app

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/arhuman/maping/server/internal/auth"
	"github.com/arhuman/maping/server/internal/control"
	"github.com/arhuman/maping/server/internal/guardrail"
	"github.com/arhuman/maping/server/internal/storage"
	"github.com/arhuman/maping/server/internal/tenant"
	"github.com/arhuman/maping/server/internal/web"

	"github.com/arhuman/maping/proto/token"
)

// controlPlane is the narrow control-plane surface the dashboard adapts: the
// self-serve key admin for the Setup panel and the onboarding state for the
// handshake stepper. *control.Store satisfies it; a fake satisfies it in tests,
// so buildWebConfig and the keyAdmin adapter are unit-testable without Postgres.
type controlPlane interface {
	IssueKey(ctx context.Context, orgID, label string) (secret string, err error)
	ListKeys(ctx context.Context, orgID string) ([]control.KeyInfo, error)
	RevokeKey(ctx context.Context, orgID, keyID string) error
	OnboardingState(ctx context.Context, tenant string) ([]control.ServiceOnboarding, error)
}

// buildWebConfig assembles the dashboard's dependencies. The tenant resolver is
// the auth seam: with auth on it renders the caller's authenticated org (read
// from the session context); with auth off it renders the constant seeded dev
// tenant, preserving the no-control-plane behavior. The key-admin panel,
// onboarding source, and CSRF key appear only when a control plane (cp) is
// present; the frozen-cardinality func only when a guard was constructed. Every
// control-plane-dependent field stays nil in static dev mode so the dashboard
// renders without Postgres.
func buildWebConfig(querier web.Querier, cp controlPlane, memberAdmin web.MemberAdmin, card *guardrail.Cardinality, authOn bool, constTenant, baseURL string, csrfKey []byte, log *slog.Logger) web.Config {
	cfg := web.Config{
		Querier: querier,
		Logger:  log,
		Tenant:  tenantResolver(authOn, constTenant),
	}
	if cp != nil {
		// Self-serve key admin for the Setup page. The adapter wraps issued secrets
		// with the deployment origin (control does not know the public URL); CSRF
		// tokens are signed with the shared session key. The team panel (MemberAdmin)
		// is composed in separately and only runs alongside a control plane.
		cfg.Onboarding = onboardingSource(cp)
		cfg.KeyAdmin = keyAdmin{cp: cp, baseURL: baseURL}
		cfg.MemberAdmin = memberAdmin
		cfg.CSRFKey = csrfKey
	}
	if authOn {
		// The team panel admin-gates its actions on the session role; the member id
		// stamps who sent an invite. Both come from the same verified session as the
		// tenant, so a member can only ever act within their own org.
		cfg.Role = sessionRole
	}
	if card != nil {
		cfg.Frozen = card.Frozen
	}
	return cfg
}

// tenantResolver builds the dashboard's per-request tenant resolver: with auth on
// it reads the authenticated org from the verified session (parsed into a tenant.ID
// at this boundary so a malformed/empty org reads as unauthenticated, not
// un-scoped); with auth off it returns the constant seeded dev tenant, parsed once.
func tenantResolver(authOn bool, constTenant string) web.TenantResolver {
	if authOn {
		return func(r *http.Request) (tenant.ID, bool) {
			s, ok := auth.TenantFromContext(r)
			if !ok {
				return tenant.ID{}, false
			}
			id, err := tenant.Parse(s)
			return id, err == nil
		}
	}
	constID, cErr := tenant.Parse(constTenant)
	return func(*http.Request) (tenant.ID, bool) { return constID, cErr == nil }
}

// sessionRole resolves the caller's role and member id from the verified session,
// for the team panel's admin gate. ok=false when there is no session.
func sessionRole(r *http.Request) (role, memberID string, ok bool) {
	s, ok := auth.FromContext(r.Context())
	if !ok {
		return "", "", false
	}
	return s.Role, s.MemberID, true
}

// onboardingSource adapts the control plane's onboarding state into web's type.
func onboardingSource(cp controlPlane) web.OnboardingSource {
	return func(ctx context.Context, tenant string) ([]web.ServiceOnboarding, error) {
		got, err := cp.OnboardingState(ctx, tenant)
		if err != nil {
			return nil, err
		}
		return mapSlice(got, func(o control.ServiceOnboarding) web.ServiceOnboarding {
			return web.ServiceOnboarding{Service: o.Service, Instance: o.Instance, HandshakeAt: o.HandshakeAt}
		}), nil
	}
}

// mapSlice maps a slice through f, the shared shape of the control→web adapters so
// each stays a single expression rather than a near-identical range loop.
func mapSlice[A, B any](in []A, f func(A) B) []B {
	out := make([]B, 0, len(in))
	for _, v := range in {
		out = append(out, f(v))
	}
	return out
}

// scopedQuerier adapts *storage.QueryService to web.Querier. The QueryService's
// own Tenant returns the concrete storage.TenantQuery, which structurally
// satisfies web.ScopedQuery but not web.Querier (whose Tenant must return the
// interface). This one-method adapter bridges that gap so storage never imports
// web (no import cycle).
type scopedQuerier struct{ qs *storage.QueryService }

func (s scopedQuerier) Tenant(id tenant.ID) web.ScopedQuery {
	return s.qs.Tenant(id)
}

// keyAdmin adapts the control plane to web.KeyAdmin: it issues/lists/revokes
// ingest keys for the Setup panel and wraps issued secrets with the deployment
// origin (the control plane does not know the server's public URL), so the web
// layer never imports the control plane or the token codec.
type keyAdmin struct {
	cp      controlPlane
	baseURL string
}

func (a keyAdmin) IssueKey(ctx context.Context, orgID, label string) (string, error) {
	secret, err := a.cp.IssueKey(ctx, orgID, label)
	if err != nil {
		return "", err
	}
	return token.Encode(a.baseURL, secret), nil
}

//nolint:dupl // parallel control→web list adapter; only the element type/mapping differ.
func (a keyAdmin) ListKeys(ctx context.Context, orgID string) ([]web.KeyInfo, error) {
	got, err := a.cp.ListKeys(ctx, orgID)
	if err != nil {
		return nil, err
	}
	return mapSlice(got, func(k control.KeyInfo) web.KeyInfo {
		return web.KeyInfo{ID: k.ID, Label: k.Label, Last4: k.Last4, CreatedAt: k.CreatedAt, RevokedAt: k.RevokedAt}
	}), nil
}

func (a keyAdmin) RevokeKey(ctx context.Context, orgID, keyID string) error {
	return a.cp.RevokeKey(ctx, orgID, keyID)
}

// buildAuth builds the dashboard auth layer, or returns (nil, nil) when there is
// no control plane (store is a nil interface) — the no-auth, constant-tenant
// path. With a store it reads OIDC credentials from the environment; the base
// URL and session-signing key are supplied by the caller (shared with the
// dashboard). Dev-login is enabled automatically inside auth.New when no real
// provider is configured, so a production deployment with GitHub/Google never
// exposes the bypass.
func buildAuth(store auth.MemberStore, interceptor auth.LoginInterceptor, baseURL string, key []byte, log *slog.Logger) (*auth.Auth, error) {
	if store == nil {
		return nil, nil
	}
	return auth.New(auth.Config{
		Store:      store,
		SessionKey: key,
		Providers:  oidcConfigFromEnv(baseURL),
		DevOrgName: devOrgName,
		Secure:     secureFromBaseURL(baseURL),
		HTTPClient: &http.Client{Timeout: 10 * time.Second},
		Logger:     log,
		// The post-auth hook is composed in (nil = plain login). A build wiring an
		// invitation feature supplies its interceptor; the public core passes none.
		Interceptor: interceptor,
	})
}
