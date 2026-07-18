package web

import (
	"context"
	"net/http"
	"time"

	"github.com/arhuman/maping/server/internal/storage"
	"github.com/arhuman/maping/server/internal/tenant"
)

// Querier is the read side the web layer depends on. It exposes no un-scoped
// query: the only way to reach the data plane is Tenant(tenant), which returns a
// tenant-bound ScopedQuery. This makes a cross-tenant read unrepresentable —
// isolation is a type property, not caller discipline. storage.TenantQuery
// structurally satisfies ScopedQuery; a fake satisfies Querier in tests, so the
// web layer never imports a live ClickHouse connection.
type Querier interface {
	Tenant(id tenant.ID) ScopedQuery
}

// ScopedQuery is the tenant-bound aggregate surface the dashboard reads. Every
// method is already scoped to the tenant the handle was created for, so no
// call site passes a tenant string.
type ScopedQuery interface {
	SeriesOverTime(ctx context.Context, service, method, route string, from, to time.Time, step time.Duration) ([]storage.TimePoint, error)
	Services(ctx context.Context, from, to time.Time) ([]storage.ServiceStat, error)
	Endpoints(ctx context.Context, service string, from, to time.Time) ([]storage.EndpointStat, error)
	EndpointDetail(ctx context.Context, service, method, route string, from, to time.Time) (storage.EndpointDetail, error)
	InstancesForEndpoint(ctx context.Context, service, method, route string, from, to time.Time) ([]storage.InstanceStat, error)
	VersionsForEndpoint(ctx context.Context, service, method, route string, from, to time.Time) ([]storage.VersionStat, error)
	ExemplarsForEndpoint(ctx context.Context, service, method, route string, from, to time.Time) ([]storage.ExemplarRow, error)
	LatencyByStatusClass(ctx context.Context, service, method, route string, from, to time.Time) (map[string]storage.ClassLatency, error)
	ErrorClassesForEndpoint(ctx context.Context, service, method, route string, from, to time.Time) ([]storage.ErrorClassStat, error)
	NoStatusReasonsForEndpoint(ctx context.Context, service, method, route string, from, to time.Time) ([]storage.NoStatusReasonStat, error)
	DownstreamForEndpoint(ctx context.Context, service, method, route string, from, to time.Time) (storage.DownstreamStat, error)
	InstanceResourcesForService(ctx context.Context, service string, from, to time.Time) ([]storage.InstanceResourceStat, error)
	MemoryTrendForService(ctx context.Context, service string, from, to time.Time, step time.Duration) ([]storage.MemoryTrendPoint, error)
	PerformanceStats(ctx context.Context, from, to time.Time) (storage.PerformanceStat, error)
	HasAnySummary(ctx context.Context) (bool, error)
}

// TenantResolver resolves the active tenant for a request. Part 1 supplies a
// constant-tenant func; Part 2 (auth) supplies the authenticated org. ok=false
// means no tenant could be resolved (unauthenticated), which the handlers turn
// into a 401 — but Part 1's constant func always returns ok=true.
type TenantResolver func(r *http.Request) (id tenant.ID, ok bool)

// ServiceOnboarding mirrors control.ServiceOnboarding without importing control
// (web sits downstream of the control plane and must not depend on it). main
// adapts the control type into this at wiring time.
type ServiceOnboarding struct {
	Service     string
	Instance    string
	HandshakeAt time.Time
}

// OnboardingSource returns the connected services for a tenant, driving the
// onboarding panel. Nil-safe: when unset (no control plane), the panel shows the
// key-valid step and nothing beyond it rather than inventing data.
type OnboardingSource func(ctx context.Context, tenant string) ([]ServiceOnboarding, error)

// FrozenFunc reports whether a tenant's cardinality is frozen on this node, so
// the onboarding/dashboard can surface the guardrail warning loudly (CONTEXT
// Guardrails). Nil-safe: unset means "no frozen signal available", not "false".
type FrozenFunc func(tenant string) bool

// KeyInfo is a listed ingest key for the Setup keys panel. It mirrors
// control.KeyInfo so the web layer never imports the control plane; main adapts
// between the two. It never carries the secret — only the display last-4 and
// lifecycle timestamps.
type KeyInfo struct {
	ID        string
	Label     string
	Last4     string
	CreatedAt time.Time
	RevokedAt *time.Time // nil while the key is active
}

// KeyAdmin is the self-serve key surface the Setup page drives: issue (returns
// the full one-time token, origin already wrapped by main), list, and revoke.
// Nil-safe: when unset (dev/no-control-plane) the keys panel is hidden and the
// key POST routes 404, so the dashboard still renders without a control plane.
type KeyAdmin interface {
	IssueKey(ctx context.Context, orgID, label string) (token string, err error)
	ListKeys(ctx context.Context, orgID string) ([]KeyInfo, error)
	RevokeKey(ctx context.Context, orgID, keyID string) error
}

// MemberInfo is a listed org member for the Setup team panel. It mirrors
// control.MemberInfo so the web layer never imports the control plane.
type MemberInfo struct {
	ID        string
	Email     string
	Role      string
	CreatedAt time.Time
	IsOwner   bool
}

// InviteInfo is a listed pending invite for the Setup team panel. It mirrors
// control.InviteInfo (the secret token is never surfaced here, only its metadata).
type InviteInfo struct {
	ID        string
	Email     string
	Role      string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// MemberAdmin is the self-serve team surface the Setup page drives: list members
// and pending invites, create an invite (returns the full one-time accept link,
// origin already wrapped by main), revoke an invite, remove a member, and read
// seat usage. Nil-safe: when unset (dev/no-control-plane) the team panel is hidden
// and its POST routes 404. Its state-changing actions are admin-gated at the
// handler (see Role).
type MemberAdmin interface {
	ListMembers(ctx context.Context, orgID string) ([]MemberInfo, error)
	ListInvites(ctx context.Context, orgID string) ([]InviteInfo, error)
	CreateInvite(ctx context.Context, orgID, invitedBy, email, role string) (link string, err error)
	RevokeInvite(ctx context.Context, orgID, inviteID string) error
	RemoveMember(ctx context.Context, orgID, memberID string) error
	SeatUsage(ctx context.Context, orgID string) (used, limit int, err error)
}

// RoleResolver reports the caller's role (and member id) for the active request,
// so the team panel can admin-gate its actions. ok=false (no control plane / no
// session) is treated as "not an admin". main supplies it from the auth session.
type RoleResolver func(r *http.Request) (role, memberID string, ok bool)
