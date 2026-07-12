//go:build integration

// Package storage integration tests exercise the real ClickHouse writer and
// query layer against a live instance. They are SKIPPED in the normal
// `go test` run (no ClickHouse in CI) and only compile under the `integration`
// build tag.
//
// Run them with a live dev stack:
//
//	make up          # starts ClickHouse from deploy/docker-compose.dev.yml
//	# apply migrations/clickhouse/0001_summaries.sql (see below)
//	go test -tags=integration ./internal/storage/...
//
// The DSN comes from MAPING_CLICKHOUSE_DSN, defaulting to the dev instance.
package storage

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/stretchr/testify/require"

	"github.com/arhuman/maping/server/internal/tenant"
)

func mustConn(t *testing.T) driver.Conn {
	t.Helper()
	cfg := ConfigFromEnv()
	opts, err := clickhouse.ParseDSN(cfg.DSN)
	require.NoError(t, err)
	conn, err := clickhouse.Open(opts)
	require.NoError(t, err)
	require.NoError(t, conn.Ping(context.Background()))
	return conn
}

// setupSchema recreates the summaries table in an isolated test database so
// runs are repeatable.
func setupSchema(t *testing.T, conn driver.Conn) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, conn.Exec(ctx, `DROP TABLE IF EXISTS summaries`))
	ddl, err := os.ReadFile("migrations/clickhouse/0001_summaries.sql")
	require.NoError(t, err)
	require.NoError(t, conn.Exec(ctx, string(ddl)))
}

// TestWriterQueryPercentiles inserts known bucket maps and asserts that
// SeriesOverTime's SQL percentiles match the frozen Go oracle
// (QuantileFromBuckets) within tolerance. This is the whole correctness point:
// the SQL math and the client math must agree.
func TestWriterQueryPercentiles(t *testing.T) {
	conn := mustConn(t)
	defer conn.Close()
	setupSchema(t, conn)

	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	w := newWriterWithConn(conn, Config{FlushInterval: 200 * time.Millisecond, FlushRows: 10}, log)

	now := time.Now().UTC().Truncate(time.Minute)
	// One time bucket, one series, a known sketch. total = 10.
	sketch := map[int32]uint64{10: 1, 20: 1, 30: 8}
	statusCodes := map[uint32]uint64{200: 10}

	row := NewRow(
		tenant.MustParse("itest-tenant"), "svc", "inst", "GET", "/x", "STATUS_CLASS_2XX",
		now, now.Add(5*time.Second),
		10, 0, 0, 0,
		sketch, statusCodes,
	)
	require.NoError(t, w.Enqueue(row))

	// Also an error row in the same bucket for the error-rate check.
	errRow := NewRow(
		tenant.MustParse("itest-tenant"), "svc", "inst", "GET", "/x", "STATUS_CLASS_5XX",
		now, now.Add(5*time.Second),
		2, 0, 0, 0,
		map[int32]uint64{30: 2}, map[uint32]uint64{500: 2},
	)
	require.NoError(t, w.Enqueue(errRow))

	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, w.Close(closeCtx))

	points, err := SeriesOverTime(
		context.Background(), conn,
		tenant.MustParse("itest-tenant"), "svc", "GET", "/x",
		now.Add(-time.Minute), now.Add(time.Minute),
		time.Minute,
	)
	require.NoError(t, err)
	require.Len(t, points, 1)

	p := points[0]
	require.Equal(t, uint64(12), p.Count)

	// Error rate: 2 (5xx) / 12 total.
	require.InDelta(t, 2.0/12.0, p.ErrorRate, 1e-6)

	// Merged sketch across both rows: {10:1, 20:1, 30:10}. total=12.
	merged := map[int32]uint64{10: 1, 20: 1, 30: 10}
	require.InDelta(t, QuantileFromBuckets(merged, 0.50), p.P50, p.P50*0.001+1e-9)
	require.InDelta(t, QuantileFromBuckets(merged, 0.95), p.P95, p.P95*0.001+1e-9)
	require.InDelta(t, QuantileFromBuckets(merged, 0.99), p.P99, p.P99*0.001+1e-9)
}

// TestDashboardAggregates exercises the M4 aggregate queries (Services,
// Endpoints, EndpointDetail) against live ClickHouse, asserting they reuse the
// frozen sumMap + percentile technique correctly over the whole window.
func TestDashboardAggregates(t *testing.T) {
	conn := mustConn(t)
	defer conn.Close()
	setupSchema(t, conn)

	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	w := newWriterWithConn(conn, Config{FlushInterval: 200 * time.Millisecond, FlushRows: 10}, log)

	now := time.Now().UTC().Truncate(time.Minute)

	// svc-a: one 2xx endpoint and one 5xx endpoint (same route) -> error rate.
	require.NoError(t, w.Enqueue(NewRow(
		tenant.MustParse("dash-tenant"), "svc-a", "inst", "GET", "/x", "STATUS_CLASS_2XX",
		now, now.Add(5*time.Second), 10, 0, 0, 0,
		map[int32]uint64{10: 1, 20: 1, 30: 8}, map[uint32]uint64{200: 10},
	)))
	require.NoError(t, w.Enqueue(NewRow(
		tenant.MustParse("dash-tenant"), "svc-a", "inst", "GET", "/x", "STATUS_CLASS_5XX",
		now, now.Add(5*time.Second), 2, 0, 0, 0,
		map[int32]uint64{30: 2}, map[uint32]uint64{500: 2},
	)))
	// svc-b: a second service so Services returns two rows ordered by count.
	require.NoError(t, w.Enqueue(NewRow(
		tenant.MustParse("dash-tenant"), "svc-b", "inst", "POST", "/y", "STATUS_CLASS_2XX",
		now, now.Add(5*time.Second), 3, 0, 0, 0,
		map[int32]uint64{40: 3}, map[uint32]uint64{201: 3},
	)))

	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, w.Close(closeCtx))

	from, to := now.Add(-time.Minute), now.Add(time.Minute)

	// Services: svc-a (12) before svc-b (3), error rate 2/12 for svc-a.
	services, err := Services(context.Background(), conn, tenant.MustParse("dash-tenant"), from, to)
	require.NoError(t, err)
	require.Len(t, services, 2)
	require.Equal(t, "svc-a", services[0].Service)
	require.Equal(t, uint64(12), services[0].Count)
	require.InDelta(t, 2.0/12.0, services[0].ErrorRate, 1e-6)
	require.Equal(t, "svc-b", services[1].Service)

	// Endpoints of svc-a: one (GET, /x) merging both classes.
	endpoints, err := Endpoints(context.Background(), conn, tenant.MustParse("dash-tenant"), "svc-a", from, to)
	require.NoError(t, err)
	require.Len(t, endpoints, 1)
	require.Equal(t, "GET", endpoints[0].Method)
	require.Equal(t, "/x", endpoints[0].Route)
	require.Equal(t, uint64(12), endpoints[0].Count)

	// EndpointDetail: merged sketch {10:1,20:1,30:10}, class + code breakdown.
	detail, err := QueryEndpointDetail(context.Background(), conn, tenant.MustParse("dash-tenant"), "svc-a", "GET", "/x", from, to)
	require.NoError(t, err)
	require.Equal(t, uint64(12), detail.Count)
	require.InDelta(t, 2.0/12.0, detail.ErrorRate, 1e-6)
	require.NotEmpty(t, detail.Histogram)
	require.Equal(t, uint64(10), detail.StatusCodes[200])
	require.Equal(t, uint64(2), detail.StatusCodes[500])

	mergedDetail := map[int32]uint64{10: 1, 20: 1, 30: 10}
	require.InDelta(t, QuantileFromBuckets(mergedDetail, 0.95), detail.P95, detail.P95*0.001+1e-9)
}
