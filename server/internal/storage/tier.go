package storage

import "time"

// Rollup tier table names. These mirror the cascading materialized views in
// migrations/clickhouse/0002_rollups.sql. The set is closed and internal, so
// selectTier can safely name the FROM table without interpolating untrusted
// input; tierTables is the allowlist that guards against any future drift.
const (
	tableRaw = "summaries"
	table1m  = "summaries_1m"
	table1h  = "summaries_1h"
	table1d  = "summaries_1d"
)

// tierTables is the allowlist of queryable source tables. selectTier only ever
// returns a member of this set; SeriesOverTime asserts membership before the
// name reaches SQL, so the table name is never attacker-controlled.
var tierTables = map[string]struct{}{
	tableRaw: {},
	table1m:  {},
	table1h:  {},
	table1d:  {},
}

// tier is a chosen source table plus a minimum sane step for its granularity.
type tier struct {
	table       string
	minStep     time.Duration
	description string
}

// selectTier picks the source rollup table and a floor step from the query
// window width [from, to). Coarser tiers cover wider windows so a year-long
// query reads day rollups, not raw 10s rows (the disk/scan win of ADR-0003).
// The returned minStep is a floor: callers may request a larger step, never a
// smaller one than the tier's own granularity.
func selectTier(from, to time.Time) tier {
	width := to.Sub(from)
	switch {
	case width <= 2*time.Hour:
		return tier{table: tableRaw, minStep: 10 * time.Second, description: "raw 10s"}
	case width <= 48*time.Hour:
		return tier{table: table1m, minStep: time.Minute, description: "1m rollup"}
	case width <= 60*24*time.Hour:
		return tier{table: table1h, minStep: time.Hour, description: "1h rollup"}
	default:
		return tier{table: table1d, minStep: 24 * time.Hour, description: "1d rollup"}
	}
}
