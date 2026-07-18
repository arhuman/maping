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
	OpenFDs         uint64  // open file-descriptor count, 0 if unavailable (gauge)
	FDLimit         uint64  // soft RLIMIT_NOFILE ceiling, 0 if unavailable (gauge)
	InFlight        uint64  // peak in-flight request concurrency during the window (gauge)
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
	OpenFDs         uint64  // peak open file-descriptor count over the window (gauge max)
	FDLimit         uint64  // peak soft RLIMIT_NOFILE ceiling over the window (gauge max)
	InFlight        uint64  // peak in-flight request concurrency over the window (gauge max)
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
    max(rss_true_bytes)    AS rss_true_bytes,
    max(open_fds)          AS open_fds,
    max(fd_limit)          AS fd_limit,
    max(in_flight)         AS in_flight
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
			&s.PostGCHeapBytes, &s.RSSTrueBytes, &s.OpenFDs, &s.FDLimit, &s.InFlight,
		}
	})
}

// MemoryTrendPoint is one time-bucket of the service's fleet memory: the peak
// post-GC live heap (the leak-vs-burst signal) and peak in-use heap over the
// bucket, across every instance/sample. Memory is a per-process property of the
// instances serving the service (instance_windows has no endpoint dimension), so
// this trend is service-scoped, not per-endpoint.
type MemoryTrendPoint struct {
	TS              time.Time
	PostGCHeapBytes uint64
	HeapInuseBytes  uint64
}

// memoryTrendQueryTemplate buckets instance_windows by the query step and takes
// the fleet peak (max over every instance and sample in the bucket) of the two
// heap gauges. A leak shows as the peak post-GC heap climbing bucket-over-bucket;
// a burst shows as a peak that spikes and returns. It is service-scoped (memory
// is per-process, not per-endpoint), tenant-scoped, and half-open over the window.
// The step is a ?-bound integer second count, never string-interpolated.
const memoryTrendQueryTemplate = `
SELECT
    toStartOfInterval(window_start, INTERVAL ? second) AS bucket,
    max(post_gc_heap_bytes) AS post_gc_heap_bytes,
    max(heap_inuse_bytes)   AS heap_inuse_bytes
FROM instance_windows
WHERE tenant = ?
  AND service = ?
  AND window_start >= ?
  AND window_start < ?
GROUP BY bucket
ORDER BY bucket`

// MemoryTrendForService returns the per-bucket fleet memory trend for a service
// over [from, to), bucketed by step, ordered by time. It reads the same
// [from, to, step] window the detail-page timeline uses so heap and latency line
// up. step is floored to one second; the caller passes the timeline step.
func MemoryTrendForService(
	ctx context.Context,
	conn driver.Conn,
	tenantID tenant.ID,
	service string,
	from, to time.Time,
	step time.Duration,
) ([]MemoryTrendPoint, error) {
	stepSeconds := int64(step / time.Second)
	if stepSeconds <= 0 {
		stepSeconds = 1
	}
	rows, err := conn.Query(ctx, memoryTrendQueryTemplate,
		stepSeconds, tenantID.String(), service, from, to,
	)
	if err != nil {
		return nil, fmt.Errorf("storage.MemoryTrendForService: query: %w", err)
	}
	defer rows.Close()

	return scanRows(rows, "MemoryTrendForService", func(p *MemoryTrendPoint) []any {
		return []any{&p.TS, &p.PostGCHeapBytes, &p.HeapInuseBytes}
	})
}
