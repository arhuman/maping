package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/arhuman/maping/server/internal/tenant"
)

// TimePoint is one time-bucket of the RED distribution: request rate inputs
// (Count over the step), the derived error rate, and p50/p95/p99 latency in
// seconds computed from the merged DDSketch buckets.
type TimePoint struct {
	TS        time.Time
	Count     uint64
	ErrorRate float64
	P50       float64
	P95       float64
	P99       float64
}

// seriesQueryTemplate is the frozen percentile SQL. It merges every summary's
// sketch within a time bucket with sumMap, then computes each quantile using the
// exact DDSketch convention shared with the client:
//
//	rank = clamp(ceil(q * total), 1, total)
//	walk buckets ascending, first cumulative count >= rank wins
//	answer = value(index) = 2 * pow(1.01, index) / 2.01  seconds
//
// arraySort(mapKeys) gives ascending indices; arrayCumSum over the aligned
// counts gives cumulative counts; indexOf(arrayMap(c -> c >= rnk, cs), 1)
// returns the 1-based position of the first bucket at or past the rank, which
// indexes back into the sorted keys. greatest(1, ...) clamps rank up; the ceil
// already caps it at total because q <= 1.
//
// The error rate is (4xx + 5xx + no_status) / total per bucket. Status classes
// are the Enum8 string values matching the proto.
// seriesQueryTemplate is the frozen percentile SQL with a single %s placeholder
// for the source rollup tier table. The table name is NEVER attacker-controlled:
// it comes only from selectTier's closed set and is re-validated against
// tierTables before formatting (belt-and-suspenders — never string-interpolate
// untrusted input).
const seriesQueryTemplate = `
WITH
    toStartOfInterval(window_start, INTERVAL ? second) AS bucket,
    sumMap(latency_sketch) AS merged,
    arraySort(mapKeys(merged)) AS ks,
    arrayMap(k -> merged[k], ks) AS vs,
    arrayCumSum(vs) AS cs,
    sum(count) AS total_count,
    sumIf(count, status_class IN ('STATUS_CLASS_4XX', 'STATUS_CLASS_5XX', 'STATUS_CLASS_NO_STATUS')) AS error_count
SELECT
    bucket AS ts,
    total_count AS cnt,
    if(total_count = 0, 0, error_count / total_count) AS error_rate,
    2 * pow(1.01, ks[indexOf(arrayMap(c -> c >= toUInt64(greatest(1, ceil(0.50 * total_count))), cs), 1)]) / 2.01 AS p50,
    2 * pow(1.01, ks[indexOf(arrayMap(c -> c >= toUInt64(greatest(1, ceil(0.95 * total_count))), cs), 1)]) / 2.01 AS p95,
    2 * pow(1.01, ks[indexOf(arrayMap(c -> c >= toUInt64(greatest(1, ceil(0.99 * total_count))), cs), 1)]) / 2.01 AS p99
FROM %s
WHERE tenant = ?
  AND service = ?
  AND (? = '' OR method = ?)
  AND (? = '' OR route_template = ?)
  AND window_start >= ?
  AND window_start < ?
GROUP BY bucket
ORDER BY bucket`

// QueryService wraps a ClickHouse connection to expose the read API as methods,
// so callers (the web layer) can depend on a small interface rather than a free
// function plus a raw driver.Conn.
type QueryService struct {
	conn driver.Conn
}

// NewQueryService builds a QueryService over an open connection.
func NewQueryService(conn driver.Conn) *QueryService {
	return &QueryService{conn: conn}
}

// TenantQuery is a QueryService bound to a single tenant. It is the ONLY way to
// reach the data-plane read API: QueryService exposes no tenant-taking methods,
// so a cross-tenant read is unrepresentable — callers must go through
// Tenant(tenant) and the bound tenant threads into every query.
type TenantQuery struct {
	s      *QueryService
	tenant tenant.ID
}

// Tenant binds the service to a tenant, returning the scoped query handle
// through which every data-plane read is issued. The tenant is a validated
// tenant.ID, so a query can never be scoped to an unvalidated identity.
func (s *QueryService) Tenant(id tenant.ID) TenantQuery {
	return TenantQuery{s: s, tenant: id}
}

// SeriesOverTime forwards to the package-level query using the wrapped
// connection and the bound tenant.
func (q TenantQuery) SeriesOverTime(
	ctx context.Context,
	service, method, route string,
	from, to time.Time,
	step time.Duration,
) ([]TimePoint, error) {
	return SeriesOverTime(ctx, q.s.conn, q.tenant, service, method, route, from, to, step)
}

// Services forwards to the package-level Services aggregate using the wrapped
// connection and the bound tenant.
func (q TenantQuery) Services(
	ctx context.Context,
	from, to time.Time,
) ([]ServiceStat, error) {
	return Services(ctx, q.s.conn, q.tenant, from, to)
}

// HasAnySummary forwards to the package-level existence check for the bound
// tenant.
func (q TenantQuery) HasAnySummary(ctx context.Context) (bool, error) {
	return HasAnySummary(ctx, q.s.conn, q.tenant)
}

// Endpoints forwards to the package-level Endpoints aggregate for the bound
// tenant.
func (q TenantQuery) Endpoints(
	ctx context.Context,
	service string,
	from, to time.Time,
) ([]EndpointStat, error) {
	return Endpoints(ctx, q.s.conn, q.tenant, service, from, to)
}

// EndpointDetail forwards to the package-level EndpointDetail aggregate for the
// bound tenant.
func (q TenantQuery) EndpointDetail(
	ctx context.Context,
	service, method, route string,
	from, to time.Time,
) (EndpointDetail, error) {
	return QueryEndpointDetail(ctx, q.s.conn, q.tenant, service, method, route, from, to)
}

// InstancesForEndpoint forwards to the package-level instance-outlier breakdown
// for the bound tenant.
func (q TenantQuery) InstancesForEndpoint(
	ctx context.Context,
	service, method, route string,
	from, to time.Time,
) ([]InstanceStat, error) {
	return InstancesForEndpoint(ctx, q.s.conn, q.tenant, service, method, route, from, to)
}

// VersionsForEndpoint forwards to the package-level per-deploy-version breakdown
// for the bound tenant.
func (q TenantQuery) VersionsForEndpoint(
	ctx context.Context,
	service, method, route string,
	from, to time.Time,
) ([]VersionStat, error) {
	return VersionsForEndpoint(ctx, q.s.conn, q.tenant, service, method, route, from, to)
}

// ExemplarsForEndpoint forwards to the package-level raw-tier exemplar breadcrumb
// query for the bound tenant.
func (q TenantQuery) ExemplarsForEndpoint(
	ctx context.Context,
	service, method, route string,
	from, to time.Time,
) ([]ExemplarRow, error) {
	return ExemplarsForEndpoint(ctx, q.s.conn, q.tenant, service, method, route, from, to)
}

// LatencyByStatusClass forwards to the package-level per-class latency split for
// the bound tenant.
func (q TenantQuery) LatencyByStatusClass(
	ctx context.Context,
	service, method, route string,
	from, to time.Time,
) (map[string]ClassLatency, error) {
	return LatencyByStatusClass(ctx, q.s.conn, q.tenant, service, method, route, from, to)
}

// ErrorClassesForEndpoint forwards to the package-level error-class breakdown for
// the bound tenant.
func (q TenantQuery) ErrorClassesForEndpoint(
	ctx context.Context,
	service, method, route string,
	from, to time.Time,
) ([]ErrorClassStat, error) {
	return ErrorClassesForEndpoint(ctx, q.s.conn, q.tenant, service, method, route, from, to)
}

// NoStatusReasonsForEndpoint forwards to the package-level NO_STATUS reason
// breakdown for the bound tenant.
func (q TenantQuery) NoStatusReasonsForEndpoint(
	ctx context.Context,
	service, method, route string,
	from, to time.Time,
) ([]NoStatusReasonStat, error) {
	return NoStatusReasonsForEndpoint(ctx, q.s.conn, q.tenant, service, method, route, from, to)
}

// PerformanceStats forwards to the package-level performance basis (represented
// requests, shipped summaries, estimated summary disk) for the bound tenant.
func (q TenantQuery) PerformanceStats(
	ctx context.Context,
	from, to time.Time,
) (PerformanceStat, error) {
	return PerformanceStats(ctx, q.s.conn, q.tenant, from, to)
}

// InstanceResourcesForService forwards to the package-level per-instance USE
// gauge breakdown for the bound tenant.
func (q TenantQuery) InstanceResourcesForService(
	ctx context.Context,
	service string,
	from, to time.Time,
) ([]InstanceResourceStat, error) {
	return InstanceResourcesForService(ctx, q.s.conn, q.tenant, service, from, to)
}

// MemoryTrendForService forwards to the package-level per-window memory trend for
// the bound tenant.
func (q TenantQuery) MemoryTrendForService(
	ctx context.Context,
	service string,
	from, to time.Time,
	step time.Duration,
) ([]MemoryTrendPoint, error) {
	return MemoryTrendForService(ctx, q.s.conn, q.tenant, service, from, to, step)
}

// DownstreamForEndpoint forwards to the package-level self-vs-downstream time
// split for the bound tenant.
func (q TenantQuery) DownstreamForEndpoint(
	ctx context.Context,
	service, method, route string,
	from, to time.Time,
) (DownstreamStat, error) {
	return DownstreamForEndpoint(ctx, q.s.conn, q.tenant, service, method, route, from, to)
}

// scanRows drains every row into a slice, scanning each into a fresh T through
// cols (which returns the scan destinations for one value), and wraps scan and
// iteration errors with the caller's op name (e.g. "Services" ->
// "storage.Services: scan: ..."). It collapses the identical
// for-rows.Next/Scan/append/rows.Err boilerplate every list query repeats.
func scanRows[T any](rows driver.Rows, op string, cols func(*T) []any) ([]T, error) {
	var out []T
	for rows.Next() {
		var v T
		if err := rows.Scan(cols(&v)...); err != nil {
			return nil, fmt.Errorf("storage.%s: scan: %w", op, err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage.%s: rows: %w", op, err)
	}
	return out, nil
}

// buildSeriesQuery resolves the rollup tier and step for the window [from, to)
// and returns the ready-to-run percentile SQL plus the bucket step in seconds.
// It does no I/O, so the tier selection, step flooring, table-allowlist guard
// (the belt-and-suspenders against ever interpolating an unvetted table name),
// and step validation are all unit-testable without a ClickHouse connection.
func buildSeriesQuery(from, to time.Time, step time.Duration) (query string, stepSeconds int64, err error) {
	// Pick the rollup tier from the window width and floor the step to the
	// tier's granularity so a coarse tier is never queried at a finer step than
	// it stores.
	t := selectTier(from, to)
	if step < t.minStep {
		step = t.minStep
	}
	if _, ok := tierTables[t.table]; !ok {
		// Unreachable for the closed tier set; asserts the invariant so a future
		// edit can never let an unvetted name reach SQL.
		return "", 0, fmt.Errorf("storage.SeriesOverTime: unknown tier table %q", t.table)
	}
	stepSeconds = int64(step / time.Second)
	if stepSeconds <= 0 {
		return "", 0, fmt.Errorf("storage.SeriesOverTime: step must be >= 1s, got %s", step)
	}
	return fmt.Sprintf(seriesQueryTemplate, t.table), stepSeconds, nil
}

// SeriesOverTime returns the per-time-bucket RED distribution for a series over
// [from, to), bucketed by step. method and route may be empty to aggregate
// across all methods / all route templates of the service. Percentiles use the
// frozen DDSketch convention above; the same math runs client-side, which is
// the whole correctness point.
func SeriesOverTime(
	ctx context.Context,
	conn driver.Conn,
	tenantID tenant.ID,
	service, method, route string,
	from, to time.Time,
	step time.Duration,
) ([]TimePoint, error) {
	query, stepSeconds, err := buildSeriesQuery(from, to, step)
	if err != nil {
		return nil, err
	}

	rows, err := conn.Query(ctx, query,
		stepSeconds,
		tenantID.String(), service,
		method, method,
		route, route,
		from, to,
	)
	if err != nil {
		return nil, fmt.Errorf("storage.SeriesOverTime: query: %w", err)
	}
	defer rows.Close()

	var out []TimePoint
	for rows.Next() {
		var (
			p         TimePoint
			cnt       uint64
			errorRate float64
		)
		if err := rows.Scan(&p.TS, &cnt, &errorRate, &p.P50, &p.P95, &p.P99); err != nil {
			return nil, fmt.Errorf("storage.SeriesOverTime: scan: %w", err)
		}
		p.Count = cnt
		p.ErrorRate = errorRate
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage.SeriesOverTime: rows: %w", err)
	}
	return out, nil
}
