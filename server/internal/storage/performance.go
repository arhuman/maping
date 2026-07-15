package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/arhuman/maping/server/internal/tenant"
)

// fallbackBytesPerSummary is the assumed on-disk size of one shipped summary row,
// used only when the real average cannot be read from system.parts (e.g. the
// grant is missing or no parts exist yet). It is deliberately conservative.
const fallbackBytesPerSummary = 400

// PerformanceStat is the real, tenant-scoped basis for the performance page:
// how many requests the tenant's stored summaries represent, how many summary
// rows were actually shipped to carry them, and an estimate of the on-disk bytes
// those summaries occupy. Requests/Summaries are measured exactly; SummaryDiskBytes
// multiplies the tenant's summary-row count by the measured average on-disk size
// of a summary row (falling back to a constant when system.parts is unavailable).
type PerformanceStat struct {
	Requests         uint64
	Summaries        uint64
	SummaryDiskBytes uint64
}

// PerformanceStats measures, for tenant over [from, to), the total requests the
// stored summaries represent (sum of the per-summary count) and the number of
// shipped summary rows, both from the raw summaries tier. It then estimates the
// on-disk size of those summaries from the measured average summary-row size
// (system.parts, best-effort — a failure falls back to a constant rather than
// failing the page). Their ratio is the compression the page reports; the caller
// projects the raw-event-pipeline size from Requests and a documented per-event
// assumption (mAPI-ng stores no raw events, so that side cannot be measured).
func PerformanceStats(
	ctx context.Context,
	conn driver.Conn,
	tenantID tenant.ID,
	from, to time.Time,
) (PerformanceStat, error) {
	var stat PerformanceStat
	row := conn.QueryRow(ctx, `
SELECT sum(count) AS requests, count() AS summaries
FROM summaries
WHERE tenant = ? AND window_start >= ? AND window_start < ?`,
		tenantID.String(), from, to,
	)
	if err := row.Scan(&stat.Requests, &stat.Summaries); err != nil {
		return PerformanceStat{}, fmt.Errorf("storage.PerformanceStats: scan counts: %w", err)
	}

	stat.SummaryDiskBytes = uint64(float64(stat.Summaries) * summaryRowBytes(ctx, conn))
	return stat, nil
}

// summaryRowBytes returns the measured average on-disk size of one raw summaries
// row from system.parts, or fallbackBytesPerSummary when that cannot be read
// (missing grant, no active parts). It is best-effort by design: the performance
// page must render even when system.parts is unavailable.
func summaryRowBytes(ctx context.Context, conn driver.Conn) float64 {
	var bytesOnDisk, rows uint64
	err := conn.QueryRow(ctx, `
SELECT sum(bytes_on_disk), sum(rows)
FROM system.parts
WHERE active AND database = currentDatabase() AND table = 'summaries'`,
	).Scan(&bytesOnDisk, &rows)
	if err != nil || rows == 0 {
		return fallbackBytesPerSummary
	}
	return float64(bytesOnDisk) / float64(rows)
}
