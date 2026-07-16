package storage

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/arhuman/maping/server/internal/tenant"
)

// This file holds the aggregate dashboard queries that feed the 3-level RED
// view. Unlike SeriesOverTime, which buckets a single series over time,
// these aggregate over the WHOLE [from, to) window and GROUP BY a dimension
// (service, or endpoint) so each row is one table row in the dashboard. They
// reuse the exact frozen sumMap + percentile technique of seriesQueryTemplate:
// merge every summary's sketch with sumMap, walk the cumulative counts, and read
// value(i) = 2*pow(1.01,i)/2.01 seconds. The error rate stays the CONTEXT
// convention: (4xx + 5xx + no_status) / total. The FROM table is only ever a
// selectTier member, re-validated against tierTables before formatting — never
// an attacker-controlled name.

// ServiceStat is one row of the service-overview level: aggregate RED metrics
// for a whole service over the query window. ErrorRate is a fraction in [0,1].
type ServiceStat struct {
	Service   string
	Count     uint64
	ErrorRate float64
	P50       float64
	P95       float64
	P99       float64
}

// EndpointStat is one row of the endpoint-table level: aggregate RED metrics
// for one (method, route_template) of a service over the query window. The
// ReqBytesAvg / RespBytesAvg fields are the per-request average payload sizes
// (sum(req_bytes)/sum(count), sum(resp_bytes)/sum(count)) for the bytes-symmetry
// view; they are appended so existing scanners and callers stay valid.
type EndpointStat struct {
	Method       string
	Route        string
	Count        uint64
	ErrorRate    float64
	P50          float64
	P95          float64
	P99          float64
	ReqBytesAvg  float64
	RespBytesAvg float64
}

// HistogramBar is one bar of the latency histogram: the bucket's latency value
// in seconds (from the frozen value(i) mapping) and the merged count in it.
type HistogramBar struct {
	LatencySeconds float64
	Count          uint64
}

// StatusClassCount is the merged request count for one status class label
// (2xx/3xx/4xx/5xx/no_status), used for the breakdown beside the error rate.
type StatusClassCount struct {
	Class string
	Count uint64
}

// EndpointDetail is the endpoint-detail level's aggregate: headline RED numbers,
// the DDSketch-derived latency histogram, the per-class breakdown, and the exact
// status-code map. All computed over the whole [from, to) window.
type EndpointDetail struct {
	Count         uint64
	ErrorRate     float64
	P50           float64
	P95           float64
	P99           float64
	Histogram     []HistogramBar
	StatusClasses []StatusClassCount
	StatusCodes   map[uint32]uint64
}

// aggregateRowCap bounds the service- and endpoint-overview aggregates. The
// dashboard renders a top-N table; a tenant with pathological service/endpoint
// cardinality must not make the overview query unbounded. Busiest rows first
// is already the ORDER BY, so the cap keeps exactly the rows the page shows.
const aggregateRowCap = 500

// percentileExpr builds the frozen quantile SQL expression against the aliases
// ks (sorted bucket indices) and cs (cumulative counts) already defined in the
// WITH clause, for quantile q against total_count. It is the same math as
// seriesQueryTemplate, factored so the aggregate queries stay in one voice.
func percentileExpr(q string) string {
	return "2 * pow(1.01, ks[indexOf(arrayMap(c -> c >= toUInt64(greatest(1, ceil(" +
		q + " * total_count))), cs), 1)]) / 2.01"
}

// servicesQueryTemplate aggregates one row per service over the whole window.
// The %s is the tier table, validated against tierTables before formatting.
var servicesQueryTemplate = `
WITH
    sumMap(latency_sketch) AS merged,
    arraySort(mapKeys(merged)) AS ks,
    arrayMap(k -> merged[k], ks) AS vs,
    arrayCumSum(vs) AS cs,
    sum(count) AS total_count,
    sumIf(count, status_class IN ('STATUS_CLASS_4XX', 'STATUS_CLASS_5XX', 'STATUS_CLASS_NO_STATUS')) AS error_count
SELECT
    service,
    total_count AS cnt,
    if(total_count = 0, 0, error_count / total_count) AS error_rate,
    ` + percentileExpr("0.50") + ` AS p50,
    ` + percentileExpr("0.95") + ` AS p95,
    ` + percentileExpr("0.99") + ` AS p99
FROM %s
WHERE tenant = ?
  AND window_start >= ?
  AND window_start < ?
GROUP BY service
ORDER BY cnt DESC
LIMIT ` + strconv.Itoa(aggregateRowCap)

// endpointsQueryTemplate aggregates one row per (method, route_template) for a
// single service over the whole window.
var endpointsQueryTemplate = `
WITH
    sumMap(latency_sketch) AS merged,
    arraySort(mapKeys(merged)) AS ks,
    arrayMap(k -> merged[k], ks) AS vs,
    arrayCumSum(vs) AS cs,
    sum(count) AS total_count,
    sumIf(count, status_class IN ('STATUS_CLASS_4XX', 'STATUS_CLASS_5XX', 'STATUS_CLASS_NO_STATUS')) AS error_count
SELECT
    method,
    route_template,
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
  AND window_start >= ?
  AND window_start < ?
GROUP BY method, route_template
ORDER BY cnt DESC
LIMIT ` + strconv.Itoa(aggregateRowCap)

// endpointDetailQueryTemplate returns the single aggregate row for one endpoint:
// the merged sketch (as sorted parallel index/count arrays for the histogram),
// the merged status_codes map, the total, and the per-class error components.
// Percentiles are computed in Go from the returned arrays via
// QuantileFromBuckets so the detail level reuses the same oracle the histogram
// is built from (one merge, one source of truth). The %s is the tier table.
var endpointDetailQueryTemplate = `
WITH
    sumMap(latency_sketch) AS merged,
    arraySort(mapKeys(merged)) AS ks,
    arrayMap(k -> merged[k], ks) AS vs,
    sumMap(status_codes) AS codes
SELECT
    sum(count) AS total_count,
    sumIf(count, status_class = 'STATUS_CLASS_2XX')       AS c2xx,
    sumIf(count, status_class = 'STATUS_CLASS_3XX')       AS c3xx,
    sumIf(count, status_class = 'STATUS_CLASS_4XX')       AS c4xx,
    sumIf(count, status_class = 'STATUS_CLASS_5XX')       AS c5xx,
    sumIf(count, status_class = 'STATUS_CLASS_NO_STATUS') AS cno,
    ks,
    vs,
    mapKeys(codes)   AS code_keys,
    mapValues(codes) AS code_vals
FROM %s
WHERE tenant = ?
  AND service = ?
  AND method = ?
  AND route_template = ?
  AND window_start >= ?
  AND window_start < ?`

// HasAnySummary reports whether tenant has at least one summary row in the raw
// tier. It drives the dashboard's onboarding gate (step 4: first data received)
// and the "show dashboard vs. onboarding panel" decision — cheap existence
// check, not a full aggregate. Uses the raw tier because onboarding cares about
// "any data ever ingested", and the raw tier is where fresh summaries land.
func HasAnySummary(ctx context.Context, conn driver.Conn, tenantID tenant.ID) (bool, error) {
	var n uint8
	row := conn.QueryRow(ctx,
		"SELECT count() > 0 FROM summaries WHERE tenant = ? LIMIT 1", tenantID.String())
	if err := row.Scan(&n); err != nil {
		return false, fmt.Errorf("storage.HasAnySummary: %w", err)
	}
	return n == 1, nil
}

// tierQuery picks the tier table for [from, to), asserts it against the
// allowlist, and formats template with it. Factored so every aggregate query
// shares the same never-interpolate-untrusted-input guard as SeriesOverTime.
func tierQuery(template string, from, to time.Time) (string, error) {
	t := selectTier(from, to)
	if _, ok := tierTables[t.table]; !ok {
		return "", fmt.Errorf("storage: unknown tier table %q", t.table)
	}
	return fmt.Sprintf(template, t.table), nil
}

// Services returns one aggregate ServiceStat per service for tenant over
// [from, to), ordered by Count descending. Percentiles use the frozen DDSketch
// convention; the error rate is the CONTEXT (4xx+5xx+no_status)/total.
func Services(
	ctx context.Context,
	conn driver.Conn,
	tenantID tenant.ID,
	from, to time.Time,
) ([]ServiceStat, error) {
	query, err := tierQuery(servicesQueryTemplate, from, to)
	if err != nil {
		return nil, fmt.Errorf("storage.Services: %w", err)
	}
	rows, err := conn.Query(ctx, query, tenantID.String(), from, to)
	if err != nil {
		return nil, fmt.Errorf("storage.Services: query: %w", err)
	}
	defer rows.Close()

	return scanRows(rows, "Services", func(s *ServiceStat) []any {
		return []any{&s.Service, &s.Count, &s.ErrorRate, &s.P50, &s.P95, &s.P99}
	})
}

// Endpoints returns one aggregate EndpointStat per (method, route_template) of
// service for tenant over [from, to), ordered by Count descending.
func Endpoints(
	ctx context.Context,
	conn driver.Conn,
	tenantID tenant.ID,
	service string,
	from, to time.Time,
) ([]EndpointStat, error) {
	query, err := tierQuery(endpointsQueryTemplate, from, to)
	if err != nil {
		return nil, fmt.Errorf("storage.Endpoints: %w", err)
	}
	rows, err := conn.Query(ctx, query, tenantID.String(), service, from, to)
	if err != nil {
		return nil, fmt.Errorf("storage.Endpoints: query: %w", err)
	}
	defer rows.Close()

	return scanRows(rows, "Endpoints", func(e *EndpointStat) []any {
		return []any{&e.Method, &e.Route, &e.Count, &e.ErrorRate, &e.P50, &e.P95, &e.P99, &e.ReqBytesAvg, &e.RespBytesAvg}
	})
}

// QueryEndpointDetail returns the full aggregate for one endpoint over
// [from, to): the merged sketch as a latency histogram, the per-class breakdown,
// the exact status-code map, and p50/p95/p99 computed in Go from the same merged
// sketch via QuantileFromBuckets — so the percentiles and the histogram can
// never disagree. Returns a zero-count EndpointDetail (no error) when the
// endpoint has no data in the window. Named QueryEndpointDetail (not
// EndpointDetail) because the return type already owns that identifier; the
// QueryService method is EndpointDetail, since methods have their own namespace.
func QueryEndpointDetail(
	ctx context.Context,
	conn driver.Conn,
	tenantID tenant.ID,
	service, method, route string,
	from, to time.Time,
) (EndpointDetail, error) {
	query, err := tierQuery(endpointDetailQueryTemplate, from, to)
	if err != nil {
		return EndpointDetail{}, fmt.Errorf("storage.EndpointDetail: %w", err)
	}
	rows, err := conn.Query(ctx, query, tenantID.String(), service, method, route, from, to)
	if err != nil {
		return EndpointDetail{}, fmt.Errorf("storage.EndpointDetail: query: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return EndpointDetail{}, fmt.Errorf("storage.EndpointDetail: rows: %w", err)
		}
		return EndpointDetail{StatusCodes: map[uint32]uint64{}}, nil
	}

	var (
		total                       uint64
		c2xx, c3xx, c4xx, c5xx, cno uint64
		ks                          []int32
		vs                          []uint64
		codeKeys                    []uint32
		codeVals                    []uint64
	)
	if err := rows.Scan(&total, &c2xx, &c3xx, &c4xx, &c5xx, &cno, &ks, &vs, &codeKeys, &codeVals); err != nil {
		return EndpointDetail{}, fmt.Errorf("storage.EndpointDetail: scan: %w", err)
	}
	if err := rows.Err(); err != nil {
		return EndpointDetail{}, fmt.Errorf("storage.EndpointDetail: rows: %w", err)
	}

	return buildEndpointDetail(total, c2xx, c3xx, c4xx, c5xx, cno, ks, vs, codeKeys, codeVals), nil
}

// buildEndpointDetail assembles the EndpointDetail from the raw scanned columns.
// It is pure (no I/O) so the histogram mapping, class rollup, and error-rate
// math are unit-testable without a live ClickHouse.
func buildEndpointDetail(
	total, c2xx, c3xx, c4xx, c5xx, cno uint64,
	ks []int32, vs []uint64,
	codeKeys []uint32, codeVals []uint64,
) EndpointDetail {
	d := EndpointDetail{
		Count:       total,
		StatusCodes: map[uint32]uint64{},
	}

	// Latency histogram: index -> value(i) seconds, keeping only non-empty
	// buckets. ks is already sorted ascending by the SQL arraySort.
	buckets := make(map[int32]uint64, len(ks))
	if len(ks) == len(vs) {
		d.Histogram = make([]HistogramBar, 0, len(ks))
		for i, idx := range ks {
			d.Histogram = append(d.Histogram, HistogramBar{
				LatencySeconds: bucketValue(idx),
				Count:          vs[i],
			})
			buckets[idx] = vs[i]
		}
	}

	// Percentiles from the same merged sketch as the histogram — one oracle.
	d.P50 = QuantileFromBuckets(buckets, 0.50)
	d.P95 = QuantileFromBuckets(buckets, 0.95)
	d.P99 = QuantileFromBuckets(buckets, 0.99)

	// Per-class breakdown, in the fixed display order.
	d.StatusClasses = []StatusClassCount{
		{Class: "2xx", Count: c2xx},
		{Class: "3xx", Count: c3xx},
		{Class: "4xx", Count: c4xx},
		{Class: "5xx", Count: c5xx},
		{Class: "no_status", Count: cno},
	}

	// Error rate = (4xx + 5xx + no_status) / total, the CONTEXT convention.
	if total > 0 {
		d.ErrorRate = float64(c4xx+c5xx+cno) / float64(total)
	}

	// Exact status-code map.
	if len(codeKeys) == len(codeVals) {
		for i, code := range codeKeys {
			d.StatusCodes[code] = codeVals[i]
		}
	}

	return d
}
