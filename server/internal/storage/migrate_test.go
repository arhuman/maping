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
