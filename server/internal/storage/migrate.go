package storage

import (
	"context"
	"embed"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// migrationsFS embeds the ClickHouse DDL that defines the data plane: the raw
// summaries tier (0001) and the rollup tiers + materialized views + TTLs (0002).
// The set is closed and internal (see tier.go); the files are the single source
// of truth for the schema, shared with the storage integration test.
//
//go:embed migrations/clickhouse/*.sql
var migrationsFS embed.FS

// ApplyMigrations runs every embedded ClickHouse migration in lexical order.
// Every statement is idempotent (CREATE ... IF NOT EXISTS, ALTER ... MODIFY
// TTL), so applying them on every startup is safe — this mirrors the control
// plane's applyMigrations and keeps mAPI-ng zero-config: a fresh ClickHouse
// gets its schema at boot rather than needing an out-of-band migrate step.
func ApplyMigrations(ctx context.Context, conn driver.Conn, log *slog.Logger) error {
	const dir = "migrations/clickhouse"
	entries, err := migrationsFS.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("storage.ApplyMigrations: read dir: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		sqlBytes, err := migrationsFS.ReadFile(dir + "/" + name)
		if err != nil {
			return fmt.Errorf("storage.ApplyMigrations: read %s: %w", name, err)
		}
		// The ClickHouse driver executes one statement per Exec, so each file is
		// split into statements. Comments are stripped first so a ';' inside a
		// comment can't split a statement (the DDL has no ';' in string literals).
		for i, stmt := range splitStatements(string(sqlBytes)) {
			if err := conn.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("storage.ApplyMigrations: apply %s stmt %d: %w", name, i+1, err)
			}
		}
		log.Info("clickhouse migration applied", slog.String("file", name))
	}
	return nil
}

// splitStatements strips '--' line comments, then splits the remaining DDL into
// individual executable statements on ';', dropping empty fragments. Stripping
// comments first is essential: a comment may itself contain a ';' (e.g. "...rows;
// the coarser bucketing..."), which would otherwise split a statement in two.
func splitStatements(sql string) []string {
	var clean strings.Builder
	for _, ln := range strings.Split(sql, "\n") {
		if i := strings.Index(ln, "--"); i >= 0 {
			ln = ln[:i]
		}
		clean.WriteString(ln)
		clean.WriteByte('\n')
	}
	var out []string
	for _, raw := range strings.Split(clean.String(), ";") {
		if stmt := strings.TrimSpace(raw); stmt != "" {
			out = append(out, stmt)
		}
	}
	return out
}
