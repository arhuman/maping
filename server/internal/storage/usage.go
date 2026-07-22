package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/arhuman/maping/server/internal/tenant"
)

// usageCardinalityWindow bounds the endpoint/series/service/instance counts and
// the request total to the last 30 days, read from the summaries_1m tier (whose
// TTL is 30 days). The raw tier is only 7 days, so it cannot answer a 30-day
// question; the 1m rollup preserves every series dimension, so uniqExact over it
// is exact for cardinality.
const usageCardinalityWindow = 30 * 24 * time.Hour

// usageDiskWindow bounds the disk-estimate basis to the raw tier's 7-day TTL: the
// estimate multiplies the tenant's raw summary-row count by the measured average
// on-disk row size (see PerformanceStats), and raw is the only tier system.parts
// sizing is calibrated against.
const usageDiskWindow = 7 * 24 * time.Hour

// TenantUsage is the operator-facing volumetry for one tenant: liveness
// (First/LastIngest), current cardinality (Endpoints/Series/Services/Instances),
// recent activity (Requests over the last 30 days), and an estimate of the on-disk
// bytes the tenant's raw summaries occupy. Series counts distinct
// (method, route_template, status_class) — the exact key the cardinality guardrail
// meters (guardrail.SeriesKey), so a caller can honestly render "series vs cap".
// A never-ingested tenant yields the zero value (zero times, zero counts).
type TenantUsage struct {
	FirstIngest time.Time
	LastIngest  time.Time
	Endpoints   uint64
	Series      uint64
	Services    uint64
	Instances   uint64
	Requests30d uint64
	DiskBytes   uint64
}

// Usage assembles the operator volumetry for tenantID as of now. It runs three
// cheap aggregates: liveness from the 1d tier (longest retention, so a churned
// tenant still shows its last-ingest date), cardinality and 30-day requests from
// the 1m tier, and the disk estimate from the raw tier via PerformanceStats. now
// is a parameter (not time.Now()) so the windows are deterministic in tests.
func Usage(ctx context.Context, conn driver.Conn, tenantID tenant.ID, now time.Time) (TenantUsage, error) {
	var u TenantUsage

	// Liveness from summaries_1d (730-day TTL): a tenant that stopped ingesting
	// weeks ago has no raw/1m rows but still resolves a real last-ingest here. n
	// distinguishes never-ingested (leave the zero times) from real epoch data.
	var n uint64
	if err := conn.QueryRow(ctx, `
SELECT min(window_start), max(window_end), count()
FROM summaries_1d
WHERE tenant = ?`, tenantID.String()).Scan(&u.FirstIngest, &u.LastIngest, &n); err != nil {
		return TenantUsage{}, fmt.Errorf("storage.Usage: liveness: %w", err)
	}
	if n == 0 {
		u.FirstIngest, u.LastIngest = time.Time{}, time.Time{}
	}

	// Cardinality and 30-day requests from summaries_1m (30-day TTL). uniqExact of
	// the (method, route_template, status_class) tuple mirrors guardrail.SeriesKey.
	if err := conn.QueryRow(ctx, `
SELECT
    uniqExact(route_template)                       AS endpoints,
    uniqExact(method, route_template, status_class) AS series,
    uniqExact(service)                              AS services,
    uniqExact(instance)                             AS instances,
    sum(count)                                      AS requests
FROM summaries_1m
WHERE tenant = ? AND window_start >= ?`,
		tenantID.String(), now.Add(-usageCardinalityWindow),
	).Scan(&u.Endpoints, &u.Series, &u.Services, &u.Instances, &u.Requests30d); err != nil {
		return TenantUsage{}, fmt.Errorf("storage.Usage: cardinality: %w", err)
	}

	// Disk estimate: reuse the performance basis (raw summary-row count times the
	// measured average on-disk row size, best-effort from system.parts).
	perf, err := PerformanceStats(ctx, conn, tenantID, now.Add(-usageDiskWindow), now)
	if err != nil {
		return TenantUsage{}, fmt.Errorf("storage.Usage: disk: %w", err)
	}
	u.DiskBytes = perf.SummaryDiskBytes
	return u, nil
}

// OperatorQuery is the cross-tenant read surface for the operator console. It is
// the deliberate counterpart to TenantQuery: where Tenant(id) scopes every read to
// one validated tenant, Operator() reaches reads that span all tenants. Keeping it
// a distinct handle (rather than adding cross-tenant methods to the scoped path)
// makes every cross-tenant read explicit at the call site and greppable in review.
type OperatorQuery struct{ s *QueryService }

// Operator returns the cross-tenant operator read handle. Reads issued through it
// are not tenant-scoped and are intended only for the allowlisted operator console.
func (s *QueryService) Operator() OperatorQuery { return OperatorQuery{s: s} }

// LastIngestByTenant forwards to the package-level cross-tenant last-ingest scan.
func (o OperatorQuery) LastIngestByTenant(ctx context.Context) (map[string]time.Time, error) {
	return LastIngestByTenant(ctx, o.s.conn)
}

// LastIngestByTenant returns the most recent ingest time for every tenant that has
// ever ingested, read in one scan of the 1d tier. It is a deliberate CROSS-TENANT
// read: unlike the tenant-scoped dashboard API (reached only through Tenant), this
// powers the operator console's account list and is exposed through the dedicated
// Operator handle, so cross-tenant reads stay explicit and greppable rather than
// smuggled into the scoped path. It returns only timestamps, never tenant metrics.
func LastIngestByTenant(ctx context.Context, conn driver.Conn) (map[string]time.Time, error) {
	rows, err := conn.Query(ctx, `
SELECT tenant, max(window_end) AS last
FROM summaries_1d
GROUP BY tenant`)
	if err != nil {
		return nil, fmt.Errorf("storage.LastIngestByTenant: query: %w", err)
	}
	defer rows.Close()

	out := make(map[string]time.Time)
	for rows.Next() {
		var t string
		var last time.Time
		if err := rows.Scan(&t, &last); err != nil {
			return nil, fmt.Errorf("storage.LastIngestByTenant: scan: %w", err)
		}
		out[t] = last
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage.LastIngestByTenant: rows: %w", err)
	}
	return out, nil
}
