package storage

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSplitStatementsEmbedded verifies every embedded ClickHouse migration
// splits into clean, executable statements — no comment leakage (a comment with
// a ';' must not produce a fragment that starts with prose) and every statement
// begins with a DDL keyword. This is the regression guard for the ClickHouse
// driver's one-statement-per-Exec contract without needing a live ClickHouse.
func TestSplitStatementsEmbedded(t *testing.T) {
	entries, err := migrationsFS.ReadDir("migrations/clickhouse")
	require.NoError(t, err)
	require.NotEmpty(t, entries)

	seen := 0
	for _, e := range entries {
		sqlBytes, err := migrationsFS.ReadFile("migrations/clickhouse/" + e.Name())
		require.NoError(t, err)
		stmts := splitStatements(string(sqlBytes))
		require.NotEmpty(t, stmts, "%s produced no statements", e.Name())
		for i, s := range stmts {
			first := strings.ToUpper(strings.Fields(s)[0])
			assert.Contains(t, []string{"CREATE", "ALTER"}, first,
				"%s stmt %d must start with a DDL keyword, got %q", e.Name(), i+1, first)
			assert.NotContains(t, s, "--", "%s stmt %d still contains a comment", e.Name(), i+1)
			seen++
		}
	}
	assert.GreaterOrEqual(t, seen, 5, "expected several statements across the migrations")
}

func TestSplitStatementsCommentWithSemicolon(t *testing.T) {
	// A comment containing a ';' must not split the following statement.
	in := "-- collapsing rows; the coarser bucketing is the win.\nCREATE TABLE t (a String);\n"
	got := splitStatements(in)
	require.Len(t, got, 1)
	assert.Equal(t, "CREATE TABLE t (a String)", got[0])
}

// TestMaterializedViewProjectionsMatchModifyQuery guards the deliberate SQL
// duplication in 0002: each rollup MV's projection is written twice — once in
// CREATE MATERIALIZED VIEW IF NOT EXISTS (builds a fresh install) and once in
// ALTER TABLE ..._mv MODIFY QUERY (updates a pre-existing MV in place). The two
// copies MUST stay byte-identical, or a fresh install and an upgraded instance
// would roll up differently. This test fails the moment they drift.
func TestMaterializedViewProjectionsMatchModifyQuery(t *testing.T) {
	sqlBytes, err := migrationsFS.ReadFile("migrations/clickhouse/0002_rollups.sql")
	require.NoError(t, err)
	stmts := splitStatements(string(sqlBytes))

	// selectBody normalizes whitespace and returns the statement from its SELECT on,
	// so the CREATE header (CREATE ... TO ... AS) and the ALTER header (ALTER ...
	// MODIFY QUERY) drop away and only the projection is compared.
	selectBody := func(s string) string {
		i := strings.Index(s, "SELECT")
		require.GreaterOrEqual(t, i, 0)
		return strings.Join(strings.Fields(s[i:]), " ")
	}

	for _, mv := range []string{"summaries_1m_mv", "summaries_1h_mv", "summaries_1d_mv"} {
		var createSel, modifySel string
		for _, s := range stmts {
			switch {
			case strings.Contains(s, "CREATE MATERIALIZED VIEW IF NOT EXISTS "+mv+" "):
				createSel = selectBody(s)
			case strings.Contains(s, "ALTER TABLE "+mv+" MODIFY QUERY"):
				modifySel = selectBody(s)
			}
		}
		require.NotEmpty(t, createSel, "missing CREATE for %s", mv)
		require.NotEmpty(t, modifySel, "missing MODIFY QUERY for %s", mv)
		assert.Equal(t, createSel, modifySel,
			"%s: CREATE and MODIFY QUERY projections must be byte-identical", mv)
	}
}
