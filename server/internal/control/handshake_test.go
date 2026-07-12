package control

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestRecordHandshake asserts the recorder issues a single Exec (the upsert)
// against the querier. The SQL correctness (ON CONFLICT keeping first_seen) is
// exercised by the integration path; here we pin the builder shape.
func TestRecordHandshake(t *testing.T) {
	q := &scriptedQuerier{}
	if err := recordHandshake(context.Background(), q, "org-1", "svc", "inst", "v1.2.3"); err != nil {
		t.Fatalf("recordHandshake: %v", err)
	}
	if q.execN != 1 {
		t.Errorf("expected 1 upsert exec, got %d", q.execN)
	}
}

// fakeRows scripts an OnboardingState result set. It embeds pgx.Rows so the
// large interface is satisfied without hand-writing every method; only Next,
// Scan, Err, and Close are meaningful here.
type fakeRows struct {
	pgx.Rows
	data []ServiceOnboarding
	pos  int
	err  error
}

func (r *fakeRows) Next() bool { return r.pos < len(r.data) }
func (r *fakeRows) Err() error { return r.err }
func (r *fakeRows) Close()     {}

func (r *fakeRows) Scan(dest ...any) error {
	o := r.data[r.pos]
	r.pos++
	*(dest[0].(*string)) = o.Service
	*(dest[1].(*string)) = o.Instance
	*(dest[2].(*time.Time)) = o.HandshakeAt
	return nil
}

// rowsOnlyQuerier is a rowsQuerier returning scripted fakeRows.
type rowsOnlyQuerier struct {
	rows *fakeRows
	err  error
}

func (q rowsOnlyQuerier) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	if q.err != nil {
		return nil, q.err
	}
	return q.rows, nil
}

func TestOnboardingState(t *testing.T) {
	now := time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)
	q := rowsOnlyQuerier{rows: &fakeRows{data: []ServiceOnboarding{
		{Service: "checkout", Instance: "pod-a", HandshakeAt: now},
		{Service: "checkout", Instance: "pod-b", HandshakeAt: now.Add(time.Minute)},
	}}}

	got, err := onboardingState(context.Background(), q, "org-1")
	if err != nil {
		t.Fatalf("onboardingState: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 rows, got %d", len(got))
	}
	if got[0].Service != "checkout" || got[0].Instance != "pod-a" {
		t.Errorf("row 0 = %+v", got[0])
	}
	if !got[1].HandshakeAt.Equal(now.Add(time.Minute)) {
		t.Errorf("row 1 handshakeAt = %v", got[1].HandshakeAt)
	}
}

func TestOnboardingStateEmpty(t *testing.T) {
	q := rowsOnlyQuerier{rows: &fakeRows{}}
	got, err := onboardingState(context.Background(), q, "org-1")
	if err != nil {
		t.Fatalf("onboardingState: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want no rows, got %d", len(got))
	}
}
