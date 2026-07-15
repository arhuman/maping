package control

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5/pgconn"
)

// recordingExecer captures every applied migration in order, so the core-then-
// extra ordering is assertable without a live Postgres.
type recordingExecer struct{ sqls []string }

func (r *recordingExecer) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	r.sqls = append(r.sqls, sql)
	return pgconn.NewCommandTag(""), nil
}

// TestApplyMigrationsRunsExtraSourcesAfterCore proves an injected source is
// applied after the embedded core schema, and that non-.sql files are skipped.
func TestApplyMigrationsRunsExtraSourcesAfterCore(t *testing.T) {
	rec := &recordingExecer{}
	extra := fstest.MapFS{
		"ent/0001_ent.sql": {Data: []byte("CREATE TABLE IF NOT EXISTS ent_widgets ();")},
		"ent/notes.txt":    {Data: []byte("not a migration")},
	}
	if err := applyMigrations(context.Background(), rec, []MigrationSource{{FS: extra, Dir: "ent"}}); err != nil {
		t.Fatalf("applyMigrations: %v", err)
	}
	if len(rec.sqls) < 2 {
		t.Fatalf("want core + extra migrations applied, got %d", len(rec.sqls))
	}
	// Core runs first: its opening file creates the orgs table.
	if !strings.Contains(rec.sqls[0], "CREATE TABLE IF NOT EXISTS orgs") {
		t.Fatalf("core migrations must run first; first sql = %q", rec.sqls[0])
	}
	// The extra source runs strictly after all of core (it is the last sql).
	if last := rec.sqls[len(rec.sqls)-1]; !strings.Contains(last, "ent_widgets") {
		t.Errorf("extra migration must run after core; last sql = %q", last)
	}
	// Non-.sql files in the source are ignored.
	for _, s := range rec.sqls {
		if strings.Contains(s, "not a migration") {
			t.Errorf("non-.sql files must be skipped, but one was applied: %q", s)
		}
	}
}

// TestWithExtraMigrationsAppendsInRegistrationOrder proves the option collects
// sources in the order they are passed to New.
func TestWithExtraMigrationsAppendsInRegistrationOrder(t *testing.T) {
	var o options
	WithExtraMigrations(fstest.MapFS{}, "a")(&o)
	WithExtraMigrations(fstest.MapFS{}, "b")(&o)
	if len(o.extraMigrations) != 2 || o.extraMigrations[0].Dir != "a" || o.extraMigrations[1].Dir != "b" {
		t.Fatalf("sources not collected in registration order: %+v", o.extraMigrations)
	}
}
