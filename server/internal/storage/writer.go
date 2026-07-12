package storage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// insertStmt is the plain INSERT used by the batcher. async_insert=1 with
// wait_for_async_insert=1 lets ClickHouse coalesce concurrent batches server
// side without a Buffer-table engine. The settings are applied
// per statement via the connection context.
const insertStmt = `INSERT INTO summaries (
    tenant, service, instance, method, route_template, status_class,
    window_start, window_end, count, sum_duration_ns, req_bytes, resp_bytes,
    latency_sketch, status_codes
)`

// ErrWriterClosed is returned by Enqueue after Close has been called.
var ErrWriterClosed = errors.New("storage: writer closed")

// Writer batches Row inserts into ClickHouse. A single background goroutine
// drains an unbounded-intent buffered channel and flushes on either the flush
// interval or the row threshold, whichever comes first. Close drains and
// flushes remaining rows within the caller's context deadline.
type Writer struct {
	conn     driver.Conn
	ownsConn bool // true only when the Writer opened the conn and must close it.
	cfg      Config
	log      *slog.Logger

	rows   chan Row
	done   chan struct{} // closed when the run loop has exited.
	closed chan struct{} // closed by Close to signal drain-and-stop.

	closeOnce sync.Once
}

// NewWriter opens a ClickHouse connection from cfg and starts the batcher
// goroutine. The caller owns the returned Writer and must call Close.
func NewWriter(ctx context.Context, cfg Config, log *slog.Logger) (*Writer, error) {
	opts, err := clickhouse.ParseDSN(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("storage.NewWriter: parse dsn: %w", err)
	}
	conn, err := clickhouse.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("storage.NewWriter: open: %w", err)
	}
	if err := conn.Ping(ctx); err != nil {
		return nil, fmt.Errorf("storage.NewWriter: ping: %w", err)
	}
	w := newWriterWithConn(conn, cfg, log) //nolint:contextcheck // the batcher runs as a detached goroutine and uses its own context, not the startup caller's.
	w.ownsConn = true                      // NewWriter opened the conn, so Close closes it.
	return w, nil
}

// newWriterWithConn wires a Writer around an already-open connection it does NOT
// own (Close drains+flushes but leaves the conn open). Split out so integration
// tests can inject a live conn (shared with the query layer) and unit tests
// exercise the row mapping without a driver. NewWriter flips ownsConn.
func newWriterWithConn(conn driver.Conn, cfg Config, log *slog.Logger) *Writer {
	if log == nil {
		log = slog.Default()
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = DefaultFlushInterval
	}
	if cfg.FlushRows <= 0 {
		cfg.FlushRows = DefaultFlushRows
	}
	if cfg.InsertTimeout <= 0 {
		cfg.InsertTimeout = DefaultInsertTimeout
	}
	w := &Writer{
		conn:   conn,
		cfg:    cfg,
		log:    log,
		rows:   make(chan Row, cfg.FlushRows),
		done:   make(chan struct{}),
		closed: make(chan struct{}),
	}
	go w.run()
	return w
}

// Migrate applies the embedded ClickHouse schema on the writer's connection.
// Keeping the raw connection inside the storage package means callers cannot run
// un-scoped SQL against the data plane: reads go through QueryService.Tenant and
// writes through Enqueue.
func (w *Writer) Migrate(ctx context.Context, log *slog.Logger) error {
	return ApplyMigrations(ctx, w.conn, log)
}

// QueryService returns a read handle over the writer's connection. Reads remain
// tenant-scoped — callers reach a query only via QueryService.Tenant(tenant) —
// so the raw connection never leaves the storage package.
func (w *Writer) QueryService() *QueryService {
	return NewQueryService(w.conn)
}

// Enqueue hands a row to the batcher. It returns ErrWriterClosed once Close has
// been called. It never blocks indefinitely: if the buffer is full it blocks
// only until the batcher drains, which is bounded by the flush cadence.
func (w *Writer) Enqueue(row Row) error {
	select {
	case <-w.closed:
		return ErrWriterClosed
	default:
	}
	select {
	case w.rows <- row:
		return nil
	case <-w.closed:
		return ErrWriterClosed
	}
}

// Close signals the batcher to drain the buffered rows and stop, then waits for
// the run loop to exit or ctx to expire.
func (w *Writer) Close(ctx context.Context) error {
	w.closeOnce.Do(func() { close(w.closed) })
	select {
	case <-w.done:
		if w.ownsConn {
			return w.conn.Close()
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("storage.Writer.Close: %w", ctx.Err())
	}
}

// run is the single batcher goroutine. It accumulates rows and flushes on the
// row threshold or the ticker, and performs a final drain on close.
//
//nolint:gocognit,gocyclo // select/flush/drain event loop; the branch count is inherent to the batcher's states.
func (w *Writer) run() {
	defer close(w.done)

	ticker := time.NewTicker(w.cfg.FlushInterval)
	defer ticker.Stop()

	batch := make([]Row, 0, w.cfg.FlushRows)

	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := w.timedInsert(batch); err != nil {
			// Explicit drop-on-failure: log and discard so a bad or stalled
			// ClickHouse does not wedge the ingest path (fail-open on the data
			// plane). The insert is timeout-bounded (cfg.InsertTimeout), so a hung
			// connection surfaces here as a deadline error rather than blocking the
			// batcher goroutine forever. Durable retry is deferred to a later milestone.
			w.log.Error("storage: batch insert failed, dropping",
				slog.Int("rows", len(batch)), slog.Any("err", err))
		}
		batch = batch[:0]
	}

	add := func(row Row) {
		batch = append(batch, row)
		if len(batch) >= w.cfg.FlushRows {
			flush()
		}
	}

	// finalFlush is the close-path flush. Unlike the steady-state flush (which
	// drops immediately so a bad ClickHouse cannot wedge live ingest), the final
	// drain retries a bounded number of times before giving up, so a deploy
	// restart does not silently lose the last buffered batch. It
	// stays time-bounded: at most finalFlushAttempts tries with a short backoff.
	finalFlush := func() {
		if len(batch) == 0 {
			return
		}
		var err error
		for attempt := 1; attempt <= finalFlushAttempts; attempt++ {
			if err = w.timedInsert(batch); err == nil {
				batch = batch[:0]
				return
			}
			w.log.Warn("storage: final drain insert failed, retrying",
				slog.Int("attempt", attempt), slog.Int("rows", len(batch)), slog.Any("err", err))
			if attempt < finalFlushAttempts {
				time.Sleep(finalFlushBackoff)
			}
		}
		w.log.Error("storage: final drain insert failed after retries, dropping",
			slog.Int("rows", len(batch)), slog.Any("err", err))
		batch = batch[:0]
	}

	for {
		select {
		case row := <-w.rows:
			add(row)
		case <-ticker.C:
			flush()
		case <-w.closed:
			w.drain(add, finalFlush)
			return
		}
	}
}

// drain empties the buffered channel into the batch (flushing on threshold) and
// performs a final flush. Called once on close; finalFlush is the bounded-retry
// flush closure supplied by run.
func (w *Writer) drain(add func(Row), finalFlush func()) {
	for {
		select {
		case row := <-w.rows:
			add(row)
		default:
			finalFlush()
			return
		}
	}
}

// timedInsert runs one batch insert bounded by cfg.InsertTimeout. The batcher
// goroutine has no ambient request context, so it derives a fresh deadline from
// Background — the deadline is what keeps a stalled connection from blocking the
// sole batcher goroutine (and, through it, all ingest) indefinitely.
func (w *Writer) timedInsert(rows []Row) error {
	ctx, cancel := context.WithTimeout(context.Background(), w.cfg.InsertTimeout)
	defer cancel()
	return w.insert(ctx, rows)
}

// insert writes one batch via a prepared ClickHouse batch with async_insert
// settings applied to the context.
func (w *Writer) insert(ctx context.Context, rows []Row) error {
	ctx = clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{
		"async_insert":          1,
		"wait_for_async_insert": 1,
	}))

	batch, err := w.conn.PrepareBatch(ctx, insertStmt)
	if err != nil {
		return fmt.Errorf("prepare batch: %w", err)
	}
	for _, r := range rows {
		if err := batch.Append(
			r.Tenant.String(), r.Service, r.Instance, r.Method, r.RouteTemplate, r.StatusClass,
			r.WindowStart, r.WindowEnd, r.Count, r.SumDurationNs, r.ReqBytes, r.RespBytes,
			r.sketchMap(), r.statusCodeMap(),
		); err != nil {
			return fmt.Errorf("append row: %w", err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("send batch: %w", err)
	}
	return nil
}
