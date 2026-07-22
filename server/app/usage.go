package app

import (
	"context"
	"time"

	"github.com/arhuman/maping/server/internal/storage"
	"github.com/arhuman/maping/server/internal/tenant"
)

// UsageStats is the operator-facing volumetry for one tenant, handed to a
// composing build through RouteContext.Usage. It mirrors storage.TenantUsage but
// lives in the public seam so an extension (which cannot import
// server/internal/storage) can name it. Series counts distinct
// (method, route_template, status_class) — the same key the cardinality guardrail
// meters — so a caller can render "series vs cap" honestly. A never-ingested
// tenant yields the zero value (zero times, zero counts).
type UsageStats struct {
	FirstIngest time.Time
	LastIngest  time.Time
	Endpoints   uint64
	Series      uint64
	Services    uint64
	Instances   uint64
	Requests30d uint64
	DiskBytes   uint64
}

// buildUsageSeam builds the two RouteContext usage closures over the data-plane
// read service: a per-tenant lookup (scoped through Tenant, so it honors the
// tenant-scoping invariant) and the cross-tenant last-ingest map (through the
// dedicated Operator handle). Both are always wired because ClickHouse is the data
// plane and is always present, unlike the optional control-plane pool.
func buildUsageSeam(qs *storage.QueryService) (
	func(ctx context.Context, tenantID string) (UsageStats, error),
	func(ctx context.Context) (map[string]time.Time, error),
) {
	usage := func(ctx context.Context, tenantID string) (UsageStats, error) {
		id, err := tenant.Parse(tenantID)
		if err != nil {
			return UsageStats{}, err
		}
		u, err := qs.Tenant(id).Usage(ctx, time.Now())
		if err != nil {
			return UsageStats{}, err
		}
		return UsageStats{
			FirstIngest: u.FirstIngest,
			LastIngest:  u.LastIngest,
			Endpoints:   u.Endpoints,
			Series:      u.Series,
			Services:    u.Services,
			Instances:   u.Instances,
			Requests30d: u.Requests30d,
			DiskBytes:   u.DiskBytes,
		}, nil
	}
	lastIngest := func(ctx context.Context) (map[string]time.Time, error) {
		return qs.Operator().LastIngestByTenant(ctx)
	}
	return usage, lastIngest
}
