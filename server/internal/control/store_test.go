package control

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/arhuman/maping/server/internal/guardrail"
)

// recordingQuerier captures the last Exec's sql+args and returns a scripted tag,
// so the key-op inserts/updates are assertable without a live Postgres.
type recordingQuerier struct {
	sql  string
	args []any
	tag  pgconn.CommandTag
	err  error
}

func (q *recordingQuerier) QueryRow(context.Context, string, ...any) pgx.Row {
	return fakeRow{err: pgx.ErrNoRows}
}

func (q *recordingQuerier) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	q.sql, q.args = sql, args
	return q.tag, q.err
}

func TestIssueKeyStoresHashOfSecret(t *testing.T) {
	q := &recordingQuerier{tag: pgconn.NewCommandTag("INSERT 0 1")}
	secret, err := issueKey(context.Background(), q, "org-1", "default")
	if err != nil {
		t.Fatalf("issueKey: %v", err)
	}
	if secret == "" || strings.Contains(secret, ".") {
		t.Fatalf("bad secret %q (must be non-empty and separator-free)", secret)
	}
	if len(q.args) != 4 {
		t.Fatalf("expected 4 insert args, got %d", len(q.args))
	}
	gotHash, ok := q.args[2].([]byte)
	if !ok {
		t.Fatalf("key_hash arg is %T, want []byte", q.args[2])
	}
	if want := hashKey(secret); !bytes.Equal(gotHash, want) {
		t.Errorf("stored hash %x, want sha256(secret) %x", gotHash, want)
	}
	if last4, _ := q.args[3].(string); last4 != secret[len(secret)-4:] {
		t.Errorf("last4 = %q, want %q", last4, secret[len(secret)-4:])
	}
}

func TestRevokeKeyActiveAndMissing(t *testing.T) {
	active := &recordingQuerier{tag: pgconn.NewCommandTag("UPDATE 1")}
	if err := revokeKey(context.Background(), active, "org-1", "key-1"); err != nil {
		t.Fatalf("revoke active: %v", err)
	}
	missing := &recordingQuerier{tag: pgconn.NewCommandTag("UPDATE 0")}
	if err := revokeKey(context.Background(), missing, "org-1", "nope"); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("revoke missing: got %v, want ErrKeyNotFound", err)
	}
}

// fakeRow scripts a single Scan result (or error) for the fake querier.
type fakeRow struct {
	values []any
	err    error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	for i := range dest {
		switch d := dest[i].(type) {
		case *string:
			*d = r.values[i].(string)
		case *float64:
			*d = r.values[i].(float64)
		case *int:
			*d = r.values[i].(int)
		case *int32:
			*d = r.values[i].(int32)
		case *int64:
			*d = r.values[i].(int64)
		default:
			return errUnsupportedScan
		}
	}
	return nil
}

var errUnsupportedScan = pgxErr("fakeRow: unsupported scan dest")

type pgxErr string

func (e pgxErr) Error() string { return string(e) }

// scriptedQuerier returns queued rows in order and records exec calls.
type scriptedQuerier struct {
	rows    []fakeRow
	execN   int
	nextRow int
}

func (q *scriptedQuerier) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	if q.nextRow >= len(q.rows) {
		return fakeRow{err: pgx.ErrNoRows}
	}
	row := q.rows[q.nextRow]
	q.nextRow++
	return row
}

func (q *scriptedQuerier) Exec(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	q.execN++
	return pgconn.CommandTag{}, nil
}

func TestQueryLimitsFound(t *testing.T) {
	q := &scriptedQuerier{rows: []fakeRow{
		{values: []any{100.0, 200, 10000, int64(4194304), 30}},
	}}
	got, err := queryLimits(context.Background(), q, "org-1")
	if err != nil {
		t.Fatalf("queryLimits: %v", err)
	}
	want := guardrail.Limits{MaxRPS: 100, Burst: 200, CardinalityCap: 10000, MaxPayloadBytes: 4194304, RetentionDays: 30}
	if got != want {
		t.Errorf("queryLimits = %+v, want %+v", got, want)
	}
}

func TestQueryLimitsMissingFallsBackToDefaults(t *testing.T) {
	q := &scriptedQuerier{rows: []fakeRow{{err: pgx.ErrNoRows}}}
	got, err := queryLimits(context.Background(), q, "unknown")
	if err != nil {
		t.Fatalf("queryLimits: %v", err)
	}
	if got != guardrail.DefaultLimits() {
		t.Errorf("missing plan should return DefaultLimits, got %+v", got)
	}
}

func TestEnsureDevKeyExistingKey(t *testing.T) {
	// First lookup returns the org id for the existing key.
	q := &scriptedQuerier{rows: []fakeRow{
		{values: []any{"org-existing"}},
	}}
	tenant, err := ensureDevKey(context.Background(), q, "dev-key", "dev-org")
	if err != nil {
		t.Fatalf("ensureDevKey: %v", err)
	}
	if tenant != "org-existing" {
		t.Errorf("tenant = %q, want org-existing", tenant)
	}
	if q.execN != 0 {
		t.Errorf("existing key must not insert, got %d execs", q.execN)
	}
}

func TestEnsureDevKeyCreatesOrgAndKey(t *testing.T) {
	// 1) key lookup -> no rows; 2) org-by-name -> no rows; 3) insert org RETURNING id.
	q := &scriptedQuerier{rows: []fakeRow{
		{err: pgx.ErrNoRows},
		{err: pgx.ErrNoRows},
		{values: []any{"org-new"}},
	}}
	tenant, err := ensureDevKey(context.Background(), q, "dev-key", "dev-org")
	if err != nil {
		t.Fatalf("ensureDevKey: %v", err)
	}
	if tenant != "org-new" {
		t.Errorf("tenant = %q, want org-new", tenant)
	}
	if q.execN != 1 {
		t.Errorf("expected 1 key insert exec, got %d", q.execN)
	}
}
