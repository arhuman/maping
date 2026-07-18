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
    latency_sketch, status_codes,
    deploy_version, deploy_id, environment, region, instance_start_time,
    max_duration_ns, exemplars,
    error_classes, no_status_reasons,
    sum_downstream_duration_ns
)`

// instanceWindowInsertStmt inserts one batch of per-instance USE gauges into the
// separate instance_windows table (see 0003). The column order matches the Append
// below.
const instanceWindowInsertStmt = `INSERT INTO instance_windows (
    tenant, service, instance, window_start, window_end,
    cpu_ns, rss_bytes, heap_alloc_bytes, gc_pause_ns, goroutines,
    num_gc, total_alloc_bytes, mallocs, gc_cpu_fraction, heap_inuse_bytes, gomaxprocs,
    post_gc_heap_bytes, rss_true_bytes
)`

// ErrWriterClosed is returned by Enqueue after Close has been called.
var ErrWriterClosed = errors.New("storage: writer closed")

// ErrEmptyTenant is returned by the enqueue methods when a row carries the
// zero-value tenant.ID. Rows are normally built from a resolved tenant (see
// ingest.summaryToRow), so the type already keeps an unvalidated tenant out of a
// query; this guards the one hole the type leaves open — a row struct-literal'd
// with the zero ID — so an un-tenanted write fails closed instead of silently
// persisting under an empty tenant. It is the write-path half of ADR-0010.
var ErrEmptyTenant = errors.New("storage: row has empty tenant")

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
	iwRows chan InstanceWindowRow // separate USE-gauge stream (instance_windows).
	done   chan struct{}          // closed when the run loop has exited.
	closed chan struct{}          // closed by Close to signal drain-and-stop.

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
		iwRows: make(chan InstanceWindowRow, cfg.FlushRows),
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

// Enqueue hands a row to the batcher. It returns ErrEmptyTenant if the row has
// no tenant (fail-closed, never persisted) and ErrWriterClosed once Close has
// been called. It never blocks indefinitely: if the buffer is full it blocks
// only until the batcher drains, which is bounded by the flush cadence.
//
//nolint:dupl // intentionally parallel to EnqueueInstanceWindow: two independent batcher streams with identical guard/close semantics; a generic merge would obscure the two-channel design.
func (w *Writer) Enqueue(row Row) error {
	if row.Tenant.IsZero() {
		return ErrEmptyTenant
	}
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

// EnqueueInstanceWindow hands one USE-gauge row to the batcher's instance-window
// stream. Like Enqueue it returns ErrWriterClosed after Close and never blocks
// indefinitely (bounded by the flush cadence).
//
//nolint:dupl // intentionally parallel to Enqueue: two independent batcher streams with identical guard/close semantics; a generic merge would obscure the two-channel design.
func (w *Writer) EnqueueInstanceWindow(row InstanceWindowRow) error {
	if row.Tenant.IsZero() {
		return ErrEmptyTenant
	}
	select {
	case <-w.closed:
		return ErrWriterClosed
	default:
	}
	select {
	case w.iwRows <- row:
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

// flushBatch inserts *batch drop-on-failure: on error it logs and discards so a
// bad or stalled ClickHouse cannot wedge live ingest (fail-open on the data
// plane). The insert is timeout-bounded (cfg.InsertTimeout), so a hung
// connection surfaces as a deadline error rather than blocking the batcher. It
// always empties *batch. Generic over the two batch element types so the summary
// and instance-window streams share one flush.
func flushBatch[T any](batch *[]T, insert func([]T) error, log *slog.Logger, what string) {
	if len(*batch) == 0 {
		return
	}
	if err := insert(*batch); err != nil {
		log.Error("storage: "+what+" insert failed, dropping",
			slog.Int("rows", len(*batch)), slog.Any("err", err))
	}
	*batch = (*batch)[:0]
}

// finalFlushBatch is the close-path flush: unlike flushBatch it retries up to
// finalFlushAttempts times (short backoff between tries) before dropping, so a
// deploy restart does not silently lose the last buffered batch. It stays
// time-bounded and always empties *batch.
func finalFlushBatch[T any](batch *[]T, insert func([]T) error, log *slog.Logger, what string) {
	if len(*batch) == 0 {
		return
	}
	var err error
	for attempt := 1; attempt <= finalFlushAttempts; attempt++ {
		if err = insert(*batch); err == nil {
			*batch = (*batch)[:0]
			return
		}
		log.Warn("storage: "+what+" final drain insert failed, retrying",
			slog.Int("attempt", attempt), slog.Int("rows", len(*batch)), slog.Any("err", err))
		if attempt < finalFlushAttempts {
			time.Sleep(finalFlushBackoff)
		}
	}
	log.Error("storage: "+what+" final drain insert failed after retries, dropping",
		slog.Int("rows", len(*batch)), slog.Any("err", err))
	*batch = (*batch)[:0]
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

	flush := func() { flushBatch(&batch, w.timedInsert, w.log, "batch") }

	add := func(row Row) {
		batch = append(batch, row)
		if len(batch) >= w.cfg.FlushRows {
			flush()
		}
	}

	finalFlush := func() { finalFlushBatch(&batch, w.timedInsert, w.log, "batch") }

	// Instance-window (USE gauge) stream: a parallel low-volume batch with the same
	// drop-on-failure steady flush and bounded-retry final flush as the summaries
	// above, kept separate because it targets a different table.
	iwBatch := make([]InstanceWindowRow, 0, w.cfg.FlushRows)
	iwFlush := func() { flushBatch(&iwBatch, w.timedInsertInstanceWindows, w.log, "instance-window") }
	iwAdd := func(row InstanceWindowRow) {
		iwBatch = append(iwBatch, row)
		if len(iwBatch) >= w.cfg.FlushRows {
			iwFlush()
		}
	}
	iwFinalFlush := func() { finalFlushBatch(&iwBatch, w.timedInsertInstanceWindows, w.log, "instance-window") }

	for {
		select {
		case row := <-w.rows:
			add(row)
		case iw := <-w.iwRows:
			iwAdd(iw)
		case <-ticker.C:
			flush()
			iwFlush()
		case <-w.closed:
			w.drain(add, finalFlush, iwAdd, iwFinalFlush)
			return
		}
	}
}

// drain empties both buffered channels into their batches (flushing on
// threshold) and performs a final flush of each. Called once on close; the
// finalFlush closures are the bounded-retry flushes supplied by run.
func (w *Writer) drain(add func(Row), finalFlush func(), iwAdd func(InstanceWindowRow), iwFinalFlush func()) {
	for {
		select {
		case row := <-w.rows:
			add(row)
		case iw := <-w.iwRows:
			iwAdd(iw)
		default:
			finalFlush()
			iwFinalFlush()
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

// timedInsertInstanceWindows runs one USE-gauge batch insert bounded by
// cfg.InsertTimeout, mirroring timedInsert for the summaries stream.
func (w *Writer) timedInsertInstanceWindows(rows []InstanceWindowRow) error {
	ctx, cancel := context.WithTimeout(context.Background(), w.cfg.InsertTimeout)
	defer cancel()
	return w.insertInstanceWindows(ctx, rows)
}

// insertInstanceWindows writes one batch of USE gauges into instance_windows with
// the same async_insert settings as the summaries insert.
func (w *Writer) insertInstanceWindows(ctx context.Context, rows []InstanceWindowRow) error {
	ctx = clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{
		"async_insert":          1,
		"wait_for_async_insert": 1,
	}))

	batch, err := w.conn.PrepareBatch(ctx, instanceWindowInsertStmt)
	if err != nil {
		return fmt.Errorf("prepare instance-window batch: %w", err)
	}
	for _, r := range rows {
		if err := batch.Append(
			r.Tenant.String(), r.Service, r.Instance, r.WindowStart, r.WindowEnd,
			r.CPUNs, r.RSSBytes, r.HeapAllocBytes, r.GCPauseNs, r.Goroutines,
			r.NumGC, r.TotalAllocBytes, r.Mallocs, r.GCCPUFraction, r.HeapInuseBytes, r.GOMAXPROCS,
			r.PostGCHeapBytes, r.RSSTrueBytes,
		); err != nil {
			return fmt.Errorf("append instance-window row: %w", err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("send instance-window batch: %w", err)
	}
	return nil
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
			r.DeployVersion, r.DeployID, r.Environment, r.Region, r.InstanceStart,
			r.MaxDurationNs, r.exemplarTuples(),
			r.errorClassMap(), r.noStatusReasonMap(),
			r.SumDownstreamNs,
		); err != nil {
			return fmt.Errorf("append row: %w", err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("send batch: %w", err)
	}
	return nil
}
