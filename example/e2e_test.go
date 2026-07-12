//go:build integration

// End-to-end test: a Gin request flows
// through the mAPI-ng client middleware, over Connect/gRPC to the REAL collector
// binary, into ClickHouse, and is read back with raw SQL.
//
// This is a black-box test: it builds and runs cmd/maping-server as a subprocess
// and only uses public APIs (client, gin, clickhouse-go), so it respects the
// server's internal-package boundary and keeps gin out of the server module.
//
// Requires a live ClickHouse (make up). Run from example/:
//
//	MAPING_CLICKHOUSE_DSN=clickhouse://maping:maping@localhost:9000/maping \
//	  go test -tags=integration -run TestEndToEnd ./...
package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	maping "github.com/arhuman/maping/client"
	mapinggin "github.com/arhuman/maping/client/gin"
)

const (
	devKey    = "dev-key"    // matches cmd/maping-server devIngestKey
	devTenant = "dev-tenant" // matches cmd/maping-server devTenant
)

func dsn() string {
	if v := os.Getenv("MAPING_CLICKHOUSE_DSN"); v != "" {
		return v
	}
	return "clickhouse://maping:maping@localhost:9000/maping"
}

func mustCH(t *testing.T) driver.Conn {
	t.Helper()
	opts, err := clickhouse.ParseDSN(dsn())
	require.NoError(t, err)
	conn, err := clickhouse.Open(opts)
	require.NoError(t, err)
	require.NoError(t, conn.Ping(context.Background()))
	return conn
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	return l.Addr().String()
}

func TestEndToEndWalkingSkeleton(t *testing.T) {
	ctx := context.Background()
	conn := mustCH(t)
	defer conn.Close()

	// Fresh schema.
	require.NoError(t, conn.Exec(ctx, `DROP TABLE IF EXISTS summaries`))
	ddl, err := os.ReadFile("../server/internal/storage/migrations/clickhouse/0001_summaries.sql")
	require.NoError(t, err)
	require.NoError(t, conn.Exec(ctx, string(ddl)))

	// Build the real collector binary (go.work resolves the local modules).
	bin := filepath.Join(t.TempDir(), "maping-server")
	build := exec.Command("go", "build", "-o", bin, "./cmd/maping-server")
	build.Dir = "../server"
	build.Env = os.Environ()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build server: %v\n%s", err, out)
	}

	// Run it against ClickHouse on a free port.
	addr := freePort(t)
	srvCtx, cancelSrv := context.WithCancel(ctx)
	defer cancelSrv()
	srv := exec.CommandContext(srvCtx, bin)
	srv.Env = append(os.Environ(),
		"MAPING_LISTEN="+addr,
		"MAPING_CLICKHOUSE_DSN="+dsn(),
	)
	srv.Stdout, srv.Stderr = os.Stdout, os.Stderr
	require.NoError(t, srv.Start())

	// Wait for readiness via /healthz.
	base := "http://" + addr
	require.Eventually(t, func() bool {
		resp, err := http.Get(base + "/healthz")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 15*time.Second, 200*time.Millisecond, "server did not become ready")

	// Instrumented Gin app pointing at the collector.
	rec := maping.NewRecorder(
		maping.WithKey(devKey),
		maping.WithEndpoint(base),
		maping.WithService("checkout"),
		maping.WithFlushWindow(500*time.Millisecond),
	)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(mapinggin.MiddlewareWithRecorder(rec))
	r.Use(gin.Recovery())
	r.GET("/hello/:name", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"hello": c.Param("name")})
	})
	app := httptest.NewServer(r)
	defer app.Close()

	// Fire N requests at the same route template.
	const n = 25
	for i := range n {
		resp, err := app.Client().Get(fmt.Sprintf("%s/hello/user-%d", app.URL, i))
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		resp.Body.Close()
	}

	// Flush the client synchronously (final flush uploads to the collector).
	flushCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	require.NoError(t, rec.Shutdown(flushCtx))

	// Gracefully stop the server so its writer drains the batch into ClickHouse.
	require.NoError(t, srv.Process.Signal(syscall.SIGTERM))
	_ = srv.Wait()

	// Read the data point back with raw SQL.
	var total uint64
	row := conn.QueryRow(ctx, `
		SELECT sum(count) FROM summaries
		WHERE tenant = ? AND service = ? AND route_template = ? AND status_class = ?`,
		devTenant, "checkout", "/hello/:name", "STATUS_CLASS_2XX")
	require.NoError(t, row.Scan(&total))
	require.Equal(t, uint64(n), total, "all fired requests must be aggregated and stored")
}
