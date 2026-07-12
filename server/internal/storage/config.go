// Package storage is the mAPI-ng data plane: the ClickHouse-backed writer and
// query layer for Summary rows. Percentiles are computed at query time from the
// merged DDSketch bucket maps, matching the frozen gamma/offset contract shared
// with the client (ADR-0001, ADR-0003).
package storage

import (
	"os"
	"time"
)

// Sketch mapping constants. These are the FROZEN DDSketch contract shared with
// the client and encoded in query.go's percentile SQL. gamma = 1.01; a bucket
// index i represents latency value(i) = 2*pow(gamma, i)/(gamma+1) seconds.
// Changing these is a breaking migration.
const (
	// SketchGamma is the DDSketch relative-error base (gamma).
	SketchGamma = 1.01
	// SketchGammaPlusOne is gamma+1, precomputed for value(i).
	SketchGammaPlusOne = SketchGamma + 1.0
)

// Batcher flush thresholds: app-side batcher, no ClickHouse Buffer engine.
const (
	// DefaultFlushInterval bounds how long a row waits before being inserted.
	DefaultFlushInterval = 2 * time.Second
	// DefaultFlushRows triggers a flush once this many rows are buffered.
	DefaultFlushRows = 100_000
	// DefaultInsertTimeout bounds a single batch insert so a ClickHouse
	// connection that stalls mid-insert cannot block the sole batcher goroutine
	// indefinitely (and, through it, wedge all ingest). It is a hang guard, not
	// an SLA: it must exceed a healthy insert's latency by a wide margin. On
	// expiry the steady-state flush drops the batch and the final drain retries.
	DefaultInsertTimeout = 30 * time.Second
)

// Final-drain retry. The close-path flush retries a bounded
// number of times before dropping, so a deploy restart does not silently lose
// the last buffered batch. The steady-state flush still drops immediately to
// avoid wedging live ingest on a bad ClickHouse.
const (
	// finalFlushAttempts bounds the close-path insert retries.
	finalFlushAttempts = 3
	// finalFlushBackoff is the pause between close-path retries.
	finalFlushBackoff = 200 * time.Millisecond
)

// defaultDSN targets the local dev ClickHouse from docker-compose.dev.yml.
const defaultDSN = "clickhouse://maping:maping@localhost:9000/maping"

// Config holds the ClickHouse connection and batcher settings.
type Config struct {
	// DSN is the ClickHouse native-protocol connection string.
	DSN string
	// FlushInterval bounds batcher latency.
	FlushInterval time.Duration
	// FlushRows triggers a flush once buffered rows reach this count.
	FlushRows int
	// InsertTimeout bounds a single batch insert so a stalled connection cannot
	// block the batcher goroutine indefinitely. Defaults to DefaultInsertTimeout.
	InsertTimeout time.Duration
}

// ConfigFromEnv builds a Config from MAPING_CLICKHOUSE_DSN, falling back to the
// local dev default.
func ConfigFromEnv() Config {
	dsn := os.Getenv("MAPING_CLICKHOUSE_DSN")
	if dsn == "" {
		dsn = defaultDSN
	}
	return Config{
		DSN:           dsn,
		FlushInterval: DefaultFlushInterval,
		FlushRows:     DefaultFlushRows,
		InsertTimeout: DefaultInsertTimeout,
	}
}
