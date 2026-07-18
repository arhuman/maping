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
	Tenant          tenant.ID
	Service         string
	Instance        string
	WindowStart     time.Time
	WindowEnd       time.Time
	CPUNs           uint64
	RSSBytes        uint64
	HeapAllocBytes  uint64
	GCPauseNs       uint64
	Goroutines      uint64
	NumGC           uint64  // completed GC cycles during the window (delta)
	TotalAllocBytes uint64  // heap bytes allocated during the window (delta)
	Mallocs         uint64  // heap objects allocated during the window (delta)
	GCCPUFraction   float64 // fraction of CPU time in GC since start (gauge, 0..1)
	HeapInuseBytes  uint64  // in-use heap bytes at sample time (gauge)
	GOMAXPROCS      uint32  // GOMAXPROCS at sample time (gauge)
	PostGCHeapBytes uint64  // live heap as of the last GC mark, post-GC baseline (gauge)
	RSSTrueBytes    uint64  // true resident set size from the OS, 0 if unavailable (gauge)
}

// InstanceResourceStat is the per-instance resource summary over the query
// window: CPU and GC-pause time summed (they are per-window deltas), and the peak
// memory / goroutine gauges (max over the window). It answers "which replica is
// saturated, and how?".
type InstanceResourceStat struct {
	Instance        string
	CPUNs           uint64
	RSSBytes        uint64
	HeapAllocBytes  uint64
	GCPauseNs       uint64
	Goroutines      uint64
	NumGC           uint64  // completed GC cycles over the window (delta sum)
	TotalAllocBytes uint64  // heap bytes allocated over the window (delta sum)
	Mallocs         uint64  // heap objects allocated over the window (delta sum)
	GCCPUFraction   float64 // average GC CPU fraction over the window (gauge avg)
	HeapInuseBytes  uint64  // peak in-use heap bytes over the window (gauge max)
	GOMAXPROCS      uint32  // peak GOMAXPROCS over the window (gauge max)
	PostGCHeapBytes uint64  // peak post-GC heap baseline over the window (gauge max)
	RSSTrueBytes    uint64  // peak true resident set size over the window (gauge max)
}

// instanceResourcesQueryTemplate aggregates one row per instance of a service
// over the window: delta counters (cpu, gc pause) sum, point-in-time gauges (rss,
// heap, goroutines) take the window peak. It reads the instance_windows table
// directly — there is a single raw tier, so no tier selection is needed. ORDER BY
// instance gives deterministic output.
const instanceResourcesQueryTemplate = `
SELECT
    instance,
    sum(cpu_ns)            AS cpu_ns,
    max(rss_bytes)         AS rss_bytes,
    max(heap_alloc_bytes)  AS heap_alloc_bytes,
    sum(gc_pause_ns)       AS gc_pause_ns,
    max(goroutines)        AS goroutines,
    sum(num_gc)            AS num_gc,
    sum(total_alloc_bytes) AS total_alloc_bytes,
    sum(mallocs)           AS mallocs,
    avg(gc_cpu_fraction)   AS gc_cpu_fraction,
    max(heap_inuse_bytes)  AS heap_inuse_bytes,
    max(gomaxprocs)        AS gomaxprocs,
    max(post_gc_heap_bytes) AS post_gc_heap_bytes,
    max(rss_true_bytes)    AS rss_true_bytes
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
		return []any{
			&s.Instance, &s.CPUNs, &s.RSSBytes, &s.HeapAllocBytes, &s.GCPauseNs, &s.Goroutines,
			&s.NumGC, &s.TotalAllocBytes, &s.Mallocs, &s.GCCPUFraction, &s.HeapInuseBytes, &s.GOMAXPROCS,
			&s.PostGCHeapBytes, &s.RSSTrueBytes,
		}
	})
}
