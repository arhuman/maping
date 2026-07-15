package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/arhuman/maping/server/internal/tenant"
)

// InstanceWindowRow is one process resource snapshot (USE gauges) ready for
// insertion into the instance_windows table: the server-resolved tenant plus
// service/instance and the per-window gauges. cpu_ns and gc_pause_ns are
// per-window deltas; the rest are point-in-time reads. It is a separate stream
// from Row (per-endpoint summaries).
type InstanceWindowRow struct {
	Tenant         tenant.ID
	Service        string
	Instance       string
	WindowStart    time.Time
	WindowEnd      time.Time
	CPUNs          uint64
	RSSBytes       uint64
	HeapAllocBytes uint64
	GCPauseNs      uint64
	Goroutines     uint64
}

// InstanceResourceStat is the per-instance resource summary over the query
// window: CPU and GC-pause time summed (they are per-window deltas), and the peak
// memory / goroutine gauges (max over the window). It answers "which replica is
// saturated, and how?".
type InstanceResourceStat struct {
	Instance       string
	CPUNs          uint64
	RSSBytes       uint64
	HeapAllocBytes uint64
	GCPauseNs      uint64
	Goroutines     uint64
}

// instanceResourcesQueryTemplate aggregates one row per instance of a service
// over the window: delta counters (cpu, gc pause) sum, point-in-time gauges (rss,
// heap, goroutines) take the window peak. It reads the instance_windows table
// directly — there is a single raw tier, so no tier selection is needed. ORDER BY
// instance gives deterministic output.
const instanceResourcesQueryTemplate = `
SELECT
    instance,
    sum(cpu_ns)           AS cpu_ns,
    max(rss_bytes)        AS rss_bytes,
    max(heap_alloc_bytes) AS heap_alloc_bytes,
    sum(gc_pause_ns)      AS gc_pause_ns,
    max(goroutines)       AS goroutines
FROM instance_windows
WHERE tenant = ?
  AND service = ?
  AND window_start >= ?
  AND window_start < ?
GROUP BY instance
ORDER BY instance`

// InstanceResourcesForService returns one InstanceResourceStat per instance of
// the service for tenant over [from, to), ordered by instance. It is the USE side
// of the debug view: saturation gauges to sit next to the RED metrics so a
// latency rise can be attributed to GC or goroutine growth without a release.
func InstanceResourcesForService(
	ctx context.Context,
	conn driver.Conn,
	tenantID tenant.ID,
	service string,
	from, to time.Time,
) ([]InstanceResourceStat, error) {
	rows, err := conn.Query(ctx, instanceResourcesQueryTemplate,
		tenantID.String(), service, from, to,
	)
	if err != nil {
		return nil, fmt.Errorf("storage.InstanceResourcesForService: query: %w", err)
	}
	defer rows.Close()

	return scanRows(rows, "InstanceResourcesForService", func(s *InstanceResourceStat) []any {
		return []any{&s.Instance, &s.CPUNs, &s.RSSBytes, &s.HeapAllocBytes, &s.GCPauseNs, &s.Goroutines}
	})
}
