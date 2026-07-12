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
func buildWebConfig(querier web.Querier, cp controlPlane, card *guardrail.Cardinality, authOn bool, constTenant, baseURL string, csrfKey []byte, log *slog.Logger) web.Config {
	cfg := web.Config{
		Querier: querier,
		Logger:  log,
	}
	if authOn {
		// The session carries the org id as a string; parse it into a tenant.ID at
		// this boundary so the dashboard's data-plane reads receive a validated
		// identity (a malformed/empty org reads as unauthenticated, not un-scoped).
		cfg.Tenant = func(r *http.Request) (tenant.ID, bool) {
			s, ok := auth.TenantFromContext(r)
			if !ok {
				return tenant.ID{}, false
			}
			id, err := tenant.Parse(s)
			return id, err == nil
		}
	} else {
		// Static dev mode: the constant seeded tenant, parsed once. An empty
		// constant resolves to unauthenticated rather than panicking at startup.
		constID, cErr := tenant.Parse(constTenant)
		cfg.Tenant = func(*http.Request) (tenant.ID, bool) { return constID, cErr == nil }
	}
	if cp != nil {
		cfg.Onboarding = func(ctx context.Context, tenant string) ([]web.ServiceOnboarding, error) {
			got, err := cp.OnboardingState(ctx, tenant)
			if err != nil {
				return nil, err
			}
			out := make([]web.ServiceOnboarding, 0, len(got))
			for _, o := range got {
				out = append(out, web.ServiceOnboarding{
					Service:     o.Service,
					Instance:    o.Instance,
					HandshakeAt: o.HandshakeAt,
				})
			}
			return out, nil
		}
		// Self-serve key management for the Setup page. The adapter wraps the
		// issued secret with the deployment origin (token.Encode) so a single
		// MAPING_KEY carries both credential and collector endpoint; CSRF tokens
		// are signed with the shared session key.
		cfg.KeyAdmin = keyAdmin{cp: cp, baseURL: baseURL}
		cfg.CSRFKey = csrfKey
	}
	if card != nil {
		cfg.Frozen = card.Frozen
	}
	return cfg
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

func (a keyAdmin) ListKeys(ctx context.Context, orgID string) ([]web.KeyInfo, error) {
	got, err := a.cp.ListKeys(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]web.KeyInfo, 0, len(got))
	for _, k := range got {
		out = append(out, web.KeyInfo{
			ID:        k.ID,
			Label:     k.Label,
			Last4:     k.Last4,
			CreatedAt: k.CreatedAt,
			RevokedAt: k.RevokedAt,
		})
	}
	return out, nil
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
func buildAuth(store auth.MemberStore, baseURL string, key []byte, log *slog.Logger) (*auth.Auth, error) {
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
	})
}
