package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/arhuman/maping/server/internal/tenant"
)

// This file holds the Phase-0 debuggability read queries: the instance-outlier
// breakdown (is a degradation one replica or fleet-wide?) and the latency split
// by status class (is the latency rise on failures or also on 2xx?). Both are
// pure read queries over columns that already exist; they reuse the same frozen
// sumMap + percentileExpr technique and the same never-interpolate-untrusted-
// input guard (tierQuery / tierTables) as the dashboard aggregates.

// statusClasses is the fixed set of status_class Enum8 values, in display order.
// LatencyByStatusClass keys its result on these exact strings so the caller
// always sees a stable, complete map even for classes with no traffic.
var statusClasses = []string{
	"STATUS_CLASS_2XX",
	"STATUS_CLASS_3XX",
	"STATUS_CLASS_4XX",
	"STATUS_CLASS_5XX",
	"STATUS_CLASS_NO_STATUS",
}

// InstanceStat is one row of the instance-outlier breakdown: aggregate RED
// metrics plus the average payload sizes for a single instance (replica) of an
// endpoint over the query window. ErrorRate is a fraction in [0,1]; the ...Avg
// fields are per-request byte averages (sum(bytes)/count).
type InstanceStat struct {
	Instance     string
	Count        uint64
	ErrorRate    float64
	P50          float64
	P95          float64
	P99          float64
	ReqBytesAvg  float64
	RespBytesAvg float64
}

// ClassLatency is the per-status-class latency split for an endpoint: the merged
// request count in the class and its p50/p95/p99 over that class's own sketch.
type ClassLatency struct {
	Count uint64
	P50   float64
	P95   float64
	P99   float64
}

// maxExemplarsPerEndpoint bounds how many exemplar breadcrumbs ExemplarsForEndpoint
// returns, so a wide window cannot flood the caller.
const maxExemplarsPerEndpoint = 20

// ExemplarRow is one real-request breadcrumb read back for the endpoint-detail
// view: the flattened exemplar plus its status class (derived at query time from
// the status code) so the caller can label errors without a second lookup.
type ExemplarRow struct {
	At         time.Time
	DurationNs uint64
	StatusCode uint32
	TraceID    string
	SpanID     string
	RequestID  string
}

// exemplarsQueryTemplate flattens the exemplars array across every matching raw
// summaries row for one endpoint, orders by latency descending, and caps the
// result. It reads the RAW summaries tier ONLY: exemplars are stored nowhere else
// (rollups drop them), so unlike the other debug queries it never goes through
// selectTier. method and route_template use the same (? = ” OR col = ?)
// optional-filter idiom. arrayJoin explodes one row per exemplar; the tuple
// element accessors (.1 … .6) match the stored column order
// (at, duration_ns, status_code, trace_id, span_id, request_id).
const exemplarsQueryTemplate = `
SELECT
    ex.1 AS at,
    ex.2 AS duration_ns,
    ex.3 AS status_code,
    ex.4 AS trace_id,
    ex.5 AS span_id,
    ex.6 AS request_id
FROM summaries
ARRAY JOIN exemplars AS ex
WHERE tenant = ?
  AND service = ?
  AND (? = '' OR method = ?)
  AND (? = '' OR route_template = ?)
  AND window_start >= ?
  AND window_start < ?
ORDER BY duration_ns DESC
LIMIT ?`

// ExemplarsForEndpoint returns up to maxExemplarsPerEndpoint real-request
// breadcrumbs for the given endpoint for tenant over [from, to), ordered by
// latency descending, so a user can pivot from a p99 / error spike to an actual
// request to open in a tracing tool or logs. method and route may be empty to
// aggregate across all methods / all route templates of the service.
//
// It reads the RAW summaries tier directly (NOT via selectTier): exemplars live
// only in the finest tier under its short TTL, so this query is only meaningful
// for recent windows within the raw retention. Older windows return no exemplars
// even though the RED aggregates remain available on the rollups.
func ExemplarsForEndpoint(
	ctx context.Context,
	conn driver.Conn,
	tenantID tenant.ID,
	service, method, route string,
	from, to time.Time,
) ([]ExemplarRow, error) {
	rows, err := conn.Query(ctx, exemplarsQueryTemplate,
		tenantID.String(), service,
		method, method,
		route, route,
		from, to,
		maxExemplarsPerEndpoint,
	)
	if err != nil {
		return nil, fmt.Errorf("storage.ExemplarsForEndpoint: query: %w", err)
	}
	defer rows.Close()

	return scanRows(rows, "ExemplarsForEndpoint", func(e *ExemplarRow) []any {
		return []any{&e.At, &e.DurationNs, &e.StatusCode, &e.TraceID, &e.SpanID, &e.RequestID}
	})
}

// VersionStat is one row of the per-deploy-version breakdown: aggregate RED
// metrics for a single deploy_version of an endpoint over the query window.
// ErrorRate is a fraction in [0,1]. It answers "did release X regress this
// endpoint?" by comparing metrics across versions.
type VersionStat struct {
	Version   string
	Count     uint64
	ErrorRate float64
	P50       float64
	P95       float64
	P99       float64
}

// instancesQueryTemplate aggregates one row per instance for one endpoint over
// the whole window. method and route_template use the same (? = ” OR col = ?)
// idiom as seriesQueryTemplate so they are optional filters. ORDER BY instance
// gives deterministic output. The %s is the tier table, validated before format.
var instancesQueryTemplate = `
WITH
    sumMap(latency_sketch) AS merged,
    arraySort(mapKeys(merged)) AS ks,
    arrayMap(k -> merged[k], ks) AS vs,
    arrayCumSum(vs) AS cs,
    sum(count) AS total_count,
    sumIf(count, status_class IN ('STATUS_CLASS_4XX', 'STATUS_CLASS_5XX', 'STATUS_CLASS_NO_STATUS')) AS error_count
SELECT
    instance,
    total_count AS cnt,
    if(total_count = 0, 0, error_count / total_count) AS error_rate,
    ` + percentileExpr("0.50") + ` AS p50,
    ` + percentileExpr("0.95") + ` AS p95,
    ` + percentileExpr("0.99") + ` AS p99,
    if(total_count = 0, 0, sum(req_bytes) / total_count)  AS req_bytes_avg,
    if(total_count = 0, 0, sum(resp_bytes) / total_count) AS resp_bytes_avg
FROM %s
WHERE tenant = ?
  AND service = ?
  AND (? = '' OR method = ?)
  AND (? = '' OR route_template = ?)
  AND window_start >= ?
  AND window_start < ?
GROUP BY instance
ORDER BY instance`

// latencyByClassQueryTemplate aggregates one row per status_class for one
// endpoint over the whole window, computing p50/p95/p99 against each class's own
// merged sketch. ORDER BY status_class gives deterministic output. The %s is the
// tier table, validated before format.
var latencyByClassQueryTemplate = `
WITH
    sumMap(latency_sketch) AS merged,
    arraySort(mapKeys(merged)) AS ks,
    arrayMap(k -> merged[k], ks) AS vs,
    arrayCumSum(vs) AS cs,
    sum(count) AS total_count
SELECT
    status_class,
    total_count AS cnt,
    ` + percentileExpr("0.50") + ` AS p50,
    ` + percentileExpr("0.95") + ` AS p95,
    ` + percentileExpr("0.99") + ` AS p99
FROM %s
WHERE tenant = ?
  AND service = ?
  AND (? = '' OR method = ?)
  AND (? = '' OR route_template = ?)
  AND window_start >= ?
  AND window_start < ?
GROUP BY status_class
ORDER BY status_class`

// versionsQueryTemplate aggregates one row per deploy_version for one endpoint
// over the whole window, reusing the frozen sumMap + percentileExpr technique.
// method and route_template use the same (? = ” OR col = ?) optional-filter
// idiom as instancesQueryTemplate. ORDER BY deploy_version gives deterministic
// output. The %s is the tier table, validated before format.
var versionsQueryTemplate = `
WITH
    sumMap(latency_sketch) AS merged,
    arraySort(mapKeys(merged)) AS ks,
    arrayMap(k -> merged[k], ks) AS vs,
    arrayCumSum(vs) AS cs,
    sum(count) AS total_count,
    sumIf(count, status_class IN ('STATUS_CLASS_4XX', 'STATUS_CLASS_5XX', 'STATUS_CLASS_NO_STATUS')) AS error_count
SELECT
    deploy_version,
    total_count AS cnt,
    if(total_count = 0, 0, error_count / total_count) AS error_rate,
    ` + percentileExpr("0.50") + ` AS p50,
    ` + percentileExpr("0.95") + ` AS p95,
    ` + percentileExpr("0.99") + ` AS p99
FROM %s
WHERE tenant = ?
  AND service = ?
  AND (? = '' OR method = ?)
  AND (? = '' OR route_template = ?)
  AND window_start >= ?
  AND window_start < ?
GROUP BY deploy_version
ORDER BY deploy_version`

// VersionsForEndpoint returns one VersionStat per deploy_version serving the
// given endpoint for tenant over [from, to), ordered by deploy_version. method
// and route may be empty to aggregate across all methods / all route templates
// of the service. It answers whether a release regressed an endpoint by putting
// each version's RED metrics side by side. Percentiles use the frozen DDSketch
// convention; the error rate is (4xx+5xx+no_status)/total.
func VersionsForEndpoint(
	ctx context.Context,
	conn driver.Conn,
	tenantID tenant.ID,
	service, method, route string,
	from, to time.Time,
) ([]VersionStat, error) {
	query, err := tierQuery(versionsQueryTemplate, from, to)
	if err != nil {
		return nil, fmt.Errorf("storage.VersionsForEndpoint: %w", err)
	}
	rows, err := conn.Query(ctx, query,
		tenantID.String(), service,
		method, method,
		route, route,
		from, to,
	)
	if err != nil {
		return nil, fmt.Errorf("storage.VersionsForEndpoint: query: %w", err)
	}
	defer rows.Close()

	return scanRows(rows, "VersionsForEndpoint", func(s *VersionStat) []any {
		return []any{&s.Version, &s.Count, &s.ErrorRate, &s.P50, &s.P95, &s.P99}
	})
}

// DownstreamStat is the self-vs-downstream time split for an endpoint over the
// window: the request count, the total request time, and the total time spent
// waiting on downstream calls. Self time is SumDurationNs - SumDownstreamNs. A
// zero SumDownstreamNs means no downstream timing was reported (the RoundTripper
// is unwired), which the UI uses to feature-gate the panel.
type DownstreamStat struct {
	Count           uint64
	SumDurationNs   uint64
	SumDownstreamNs uint64
}

// downstreamQueryTemplate sums the request time and downstream wait time for one
// endpoint over the whole window into a single row. Same tier/filter conventions
// as the other debug queries. The %s is the tier table, validated before format.
var downstreamQueryTemplate = `
SELECT
    sum(count)                      AS cnt,
    sum(sum_duration_ns)            AS sum_duration_ns,
    sum(sum_downstream_duration_ns) AS sum_downstream_ns
FROM %s
WHERE tenant = ?
  AND service = ?
  AND (? = '' OR method = ?)
  AND (? = '' OR route_template = ?)
  AND window_start >= ?
  AND window_start < ?`

// DownstreamForEndpoint returns the self-vs-downstream time split for the given
// endpoint for tenant over [from, to). method and route may be empty to aggregate
// across all methods / all route templates of the service. It answers "how much
// of this endpoint's latency is its own work vs waiting on a dependency?".
func DownstreamForEndpoint(
	ctx context.Context,
	conn driver.Conn,
	tenantID tenant.ID,
	service, method, route string,
	from, to time.Time,
) (DownstreamStat, error) {
	query, err := tierQuery(downstreamQueryTemplate, from, to)
	if err != nil {
		return DownstreamStat{}, fmt.Errorf("storage.DownstreamForEndpoint: %w", err)
	}
	var s DownstreamStat
	row := conn.QueryRow(ctx, query,
		tenantID.String(), service,
		method, method,
		route, route,
		from, to,
	)
	if err := row.Scan(&s.Count, &s.SumDurationNs, &s.SumDownstreamNs); err != nil {
		return DownstreamStat{}, fmt.Errorf("storage.DownstreamForEndpoint: scan: %w", err)
	}
	return s, nil
}

// InstancesForEndpoint returns one InstanceStat per instance (replica) serving
// the given endpoint for tenant over [from, to), ordered by instance. method and
// route may be empty to aggregate across all methods / all route templates of
// the service. This is the flagship outlier query: it answers whether a
// degradation is confined to one replica or is fleet-wide. Percentiles use the
// frozen DDSketch convention; the error rate is (4xx+5xx+no_status)/total.
func InstancesForEndpoint(
	ctx context.Context,
	conn driver.Conn,
	tenantID tenant.ID,
	service, method, route string,
	from, to time.Time,
) ([]InstanceStat, error) {
	query, err := tierQuery(instancesQueryTemplate, from, to)
	if err != nil {
		return nil, fmt.Errorf("storage.InstancesForEndpoint: %w", err)
	}
	rows, err := conn.Query(ctx, query,
		tenantID.String(), service,
		method, method,
		route, route,
		from, to,
	)
	if err != nil {
		return nil, fmt.Errorf("storage.InstancesForEndpoint: query: %w", err)
	}
	defer rows.Close()

	return scanRows(rows, "InstancesForEndpoint", func(s *InstanceStat) []any {
		return []any{&s.Instance, &s.Count, &s.ErrorRate, &s.P50, &s.P95, &s.P99, &s.ReqBytesAvg, &s.RespBytesAvg}
	})
}

// LatencyByStatusClass returns the per-status-class latency split for one
// endpoint for tenant over [from, to). The returned map is keyed on the
// status_class Enum8 values (STATUS_CLASS_2XX, _3XX, _4XX, _5XX, _NO_STATUS) and
// always contains every class: classes with no traffic in the window map to a
// zero-valued ClassLatency, so the caller sees a stable, complete shape. It
// answers whether a latency rise is on failures or also on successes. method and
// route may be empty to aggregate across all methods / all route templates.
func LatencyByStatusClass(
	ctx context.Context,
	conn driver.Conn,
	tenantID tenant.ID,
	service, method, route string,
	from, to time.Time,
) (map[string]ClassLatency, error) {
	query, err := tierQuery(latencyByClassQueryTemplate, from, to)
	if err != nil {
		return nil, fmt.Errorf("storage.LatencyByStatusClass: %w", err)
	}
	rows, err := conn.Query(ctx, query,
		tenantID.String(), service,
		method, method,
		route, route,
		from, to,
	)
	if err != nil {
		return nil, fmt.Errorf("storage.LatencyByStatusClass: query: %w", err)
	}
	defer rows.Close()

	out := make(map[string]ClassLatency, len(statusClasses))
	for _, c := range statusClasses {
		out[c] = ClassLatency{}
	}
	for rows.Next() {
		var (
			class string
			cl    ClassLatency
		)
		if err := rows.Scan(&class, &cl.Count, &cl.P50, &cl.P95, &cl.P99); err != nil {
			return nil, fmt.Errorf("storage.LatencyByStatusClass: scan: %w", err)
		}
		// Only keep known classes so a stray Enum value can never inflate the map.
		if _, ok := out[class]; ok {
			out[class] = cl
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage.LatencyByStatusClass: rows: %w", err)
	}
	return out, nil
}
