package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/arhuman/maping/server/internal/tenant"
)

// This file holds the Phase-3 debuggability read queries: the error-class
// breakdown (what is behind the errors?) and the NO_STATUS reason breakdown
// (timing out vs canceling vs crashing?). Both merge the bounded maps stored per
// row with sumMap — so they work on any rollup tier — then explode the merged
// map into one row per key, reusing the same tierQuery guard and optional
// method/route filter idiom as the other debug queries.

// maxErrorClassesPerEndpoint bounds how many error-class rows
// ErrorClassesForEndpoint returns, so a wide window cannot flood the caller.
const maxErrorClassesPerEndpoint = 20

// ErrorClassStat is one row of the error-class breakdown for an endpoint: a
// normalized error label and how many requests carried it in the window. It
// answers "5xx up because of what?" ("DB_POOL_EXHAUSTED" vs "UPSTREAM_TIMEOUT").
type ErrorClassStat struct {
	Class string
	Count uint64
}

// NoStatusReasonStat is one row of the NO_STATUS reason breakdown: the proto
// NoStatusReason enum value (as UInt8) and how many aborted requests it explains,
// telling apart timing-out vs canceling vs crashing.
type NoStatusReasonStat struct {
	Reason uint8
	Count  uint64
}

// errorClassesQueryTemplate merges the error_classes maps across every matching
// row (sumMap, so it works on any tier — the column rolls up), then explodes the
// merged map into one row per label ordered by count. method and route_template
// use the same (? = ” OR col = ?) optional-filter idiom. The %s is the tier
// table, validated before format.
var errorClassesQueryTemplate = `
SELECT class, cnt
FROM (
    SELECT sumMap(error_classes) AS merged
    FROM %s
    WHERE tenant = ?
      AND service = ?
      AND (? = '' OR method = ?)
      AND (? = '' OR route_template = ?)
      AND window_start >= ?
      AND window_start < ?
)
ARRAY JOIN mapKeys(merged) AS class, mapValues(merged) AS cnt
ORDER BY cnt DESC, class
LIMIT ?`

// noStatusReasonsQueryTemplate merges the no_status_reasons maps across matching
// rows and explodes them into one row per reason. Same tier/filter conventions as
// errorClassesQueryTemplate; the reason domain is tiny so it needs no LIMIT.
var noStatusReasonsQueryTemplate = `
SELECT reason, cnt
FROM (
    SELECT sumMap(no_status_reasons) AS merged
    FROM %s
    WHERE tenant = ?
      AND service = ?
      AND (? = '' OR method = ?)
      AND (? = '' OR route_template = ?)
      AND window_start >= ?
      AND window_start < ?
)
ARRAY JOIN mapKeys(merged) AS reason, mapValues(merged) AS cnt
ORDER BY reason`

// ErrorClassesForEndpoint returns the top error-class labels attached to the
// given endpoint for tenant over [from, to), ordered by request count descending
// and capped at maxErrorClassesPerEndpoint. method and route may be empty to
// aggregate across all methods / all route templates of the service.
func ErrorClassesForEndpoint(
	ctx context.Context,
	conn driver.Conn,
	tenantID tenant.ID,
	service, method, route string,
	from, to time.Time,
) ([]ErrorClassStat, error) {
	query, err := tierQuery(errorClassesQueryTemplate, from, to)
	if err != nil {
		return nil, fmt.Errorf("storage.ErrorClassesForEndpoint: %w", err)
	}
	rows, err := conn.Query(ctx, query,
		tenantID.String(), service,
		method, method,
		route, route,
		from, to,
		maxErrorClassesPerEndpoint,
	)
	if err != nil {
		return nil, fmt.Errorf("storage.ErrorClassesForEndpoint: query: %w", err)
	}
	defer rows.Close()

	var out []ErrorClassStat
	for rows.Next() {
		var s ErrorClassStat
		if err := rows.Scan(&s.Class, &s.Count); err != nil {
			return nil, fmt.Errorf("storage.ErrorClassesForEndpoint: scan: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage.ErrorClassesForEndpoint: rows: %w", err)
	}
	return out, nil
}

// NoStatusReasonsForEndpoint returns the NO_STATUS reason breakdown for the given
// endpoint for tenant over [from, to), ordered by reason. method and route may be
// empty to aggregate across all methods / all route templates of the service.
func NoStatusReasonsForEndpoint(
	ctx context.Context,
	conn driver.Conn,
	tenantID tenant.ID,
	service, method, route string,
	from, to time.Time,
) ([]NoStatusReasonStat, error) {
	query, err := tierQuery(noStatusReasonsQueryTemplate, from, to)
	if err != nil {
		return nil, fmt.Errorf("storage.NoStatusReasonsForEndpoint: %w", err)
	}
	rows, err := conn.Query(ctx, query,
		tenantID.String(), service,
		method, method,
		route, route,
		from, to,
	)
	if err != nil {
		return nil, fmt.Errorf("storage.NoStatusReasonsForEndpoint: query: %w", err)
	}
	defer rows.Close()

	var out []NoStatusReasonStat
	for rows.Next() {
		var s NoStatusReasonStat
		if err := rows.Scan(&s.Reason, &s.Count); err != nil {
			return nil, fmt.Errorf("storage.NoStatusReasonsForEndpoint: scan: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage.NoStatusReasonsForEndpoint: rows: %w", err)
	}
	return out, nil
}
