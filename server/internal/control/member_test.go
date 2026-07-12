package control

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

// fakeTx is a fake pgx.Tx that scripts QueryRow results in order and records
// Commit/Rollback. It embeds pgx.Tx so it satisfies the interface without
// implementing the unused methods (they are never called by the code under
// test; calling one would panic on the nil embedded value, flagging misuse).
type fakeTx struct {
	pgx.Tx
	rows       []fakeRow
	nextRow    int
	committed  bool
	rolledBack bool
}

func (t *fakeTx) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	if t.nextRow >= len(t.rows) {
		return fakeRow{err: pgx.ErrNoRows}
	}
	row := t.rows[t.nextRow]
	t.nextRow++
	return row
}

func (t *fakeTx) Commit(context.Context) error   { t.committed = true; return nil }
func (t *fakeTx) Rollback(context.Context) error { t.rolledBack = true; return nil }

// fakeTxBeginner hands out a single scripted fakeTx.
type fakeTxBeginner struct{ tx *fakeTx }

func (b *fakeTxBeginner) Begin(context.Context) (pgx.Tx, error) { return b.tx, nil }

func TestUpsertMemberFromOIDCExisting(t *testing.T) {
	// First (and only) query: existing member lookup returns id, org, role.
	tx := &fakeTx{rows: []fakeRow{
		{values: []any{"member-1", "org-1", "member"}},
	}}
	org, mem, role, isNew, err := upsertMemberFromOIDC(context.Background(), &fakeTxBeginner{tx: tx}, "github:1", "a@b.c")
	if err != nil {
		t.Fatalf("upsertMemberFromOIDC: %v", err)
	}
	if org != "org-1" || mem != "member-1" || role != "member" {
		t.Errorf("got (%q,%q,%q), want (org-1,member-1,member)", org, mem, role)
	}
	if isNew {
		t.Error("existing member must report isNew=false")
	}
	if !tx.committed {
		t.Error("existing-member path must commit")
	}
}

func TestUpsertMemberFromOIDCFirstLoginCreatesAdminOrgOfOne(t *testing.T) {
	// 1) member lookup -> no rows; 2) INSERT org RETURNING id; 3) INSERT member RETURNING id.
	tx := &fakeTx{rows: []fakeRow{
		{err: pgx.ErrNoRows},
		{values: []any{"org-new"}},
		{values: []any{"member-new"}},
	}}
	org, mem, role, isNew, err := upsertMemberFromOIDC(context.Background(), &fakeTxBeginner{tx: tx}, "google:sub", "solo@user.dev")
	if err != nil {
		t.Fatalf("upsertMemberFromOIDC: %v", err)
	}
	if org != "org-new" || mem != "member-new" {
		t.Errorf("got org=%q member=%q, want org-new/member-new", org, mem)
	}
	if role != "admin" {
		t.Errorf("first user must be admin, got %q", role)
	}
	if !isNew {
		t.Error("first login must report isNew=true")
	}
	if !tx.committed {
		t.Error("first-login path must commit after creating org+member")
	}
}

func TestDevOrgAdminReusesExistingMember(t *testing.T) {
	// 1) org-by-name -> org id; 2) member-by-subject -> member id (exists).
	q := &scriptedQuerier{rows: []fakeRow{
		{values: []any{"dev-org-id"}},
		{values: []any{"dev-member-id"}},
	}}
	org, mem, err := devOrgAdmin(context.Background(), q, "dev-org")
	if err != nil {
		t.Fatalf("devOrgAdmin: %v", err)
	}
	if org != "dev-org-id" || mem != "dev-member-id" {
		t.Errorf("got (%q,%q), want (dev-org-id,dev-member-id)", org, mem)
	}
	if q.execN != 0 {
		t.Errorf("existing dev member must not insert, got %d execs", q.execN)
	}
}

func TestDevOrgAdminCreatesMissingMember(t *testing.T) {
	// 1) org-by-name -> org id; 2) member-by-subject -> no rows; 3) INSERT member RETURNING id.
	q := &scriptedQuerier{rows: []fakeRow{
		{values: []any{"dev-org-id"}},
		{err: pgx.ErrNoRows},
		{values: []any{"dev-member-new"}},
	}}
	org, mem, err := devOrgAdmin(context.Background(), q, "dev-org")
	if err != nil {
		t.Fatalf("devOrgAdmin: %v", err)
	}
	if org != "dev-org-id" || mem != "dev-member-new" {
		t.Errorf("got (%q,%q), want (dev-org-id,dev-member-new)", org, mem)
	}
}

func TestDevOrgAdminMissingOrg(t *testing.T) {
	q := &scriptedQuerier{rows: []fakeRow{{err: pgx.ErrNoRows}}}
	if _, _, err := devOrgAdmin(context.Background(), q, "dev-org"); err == nil {
		t.Fatal("expected error when dev org is not seeded")
	}
}
