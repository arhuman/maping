// Package control is the mAPI-ng control plane: the Postgres-backed store of
// orgs (tenants), ingest keys, members, and plan limits. Ingest validates keys
// against it (cached) and resolves the tenant; the guardrail layer reads a
// tenant's plan limits from it. It sits above guardrail in the data flow
// (control depends on guardrail for the shared Limits type, never the reverse)
// and it is never imported by ingest — main wires control.Resolver in as an
// ingest.KeyResolver, so ingest stays control-agnostic.
package control

import (
	"context"
	"crypto/sha256"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arhuman/maping/proto/token"
	"github.com/arhuman/maping/server/internal/guardrail"
)

// ErrKeyNotFound is returned by RevokeKey when no active key matches the org and
// id (already revoked, or never existed).
var ErrKeyNotFound = errors.New("control: ingest key not found or already revoked")

// KeyInfo is a listed ingest key for the dashboard key panel. It never carries
// the secret — only the display-only last-4 fragment and lifecycle timestamps.
type KeyInfo struct {
	ID        string
	Label     string
	Last4     string
	CreatedAt time.Time
	RevokedAt *time.Time // nil while the key is active
}

//go:embed migrations/postgres/*.sql
var migrationsFS embed.FS

// MigrationSource is a directory of ordered .sql files applied on top of the
// embedded core schema. It is the extension seam for a composed build (e.g. an
// enterprise module) to add its own tables/columns without editing core
// migrations: the files must be additive and idempotent, exactly like core.
type MigrationSource struct {
	FS  fs.FS
	Dir string
}

// options carries the functional-option configuration for New.
type options struct {
	extraMigrations []MigrationSource
}

// Option configures New.
type Option func(*options)

// WithExtraMigrations registers an additional migration source applied, in
// lexical filename order, AFTER the embedded core migrations. Multiple sources
// apply in registration order. This lets a composed build layer extra schema on
// top of core without forking the core migration history.
func WithExtraMigrations(fsys fs.FS, dir string) Option {
	return func(o *options) {
		o.extraMigrations = append(o.extraMigrations, MigrationSource{FS: fsys, Dir: dir})
	}
}

// querier is the subset of pgxpool.Pool the store needs, so the resolver and
// limits lookups can be unit-tested against a fake without a live Postgres.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Store wraps a pgx connection pool over the control-plane schema. New pings the
// database and applies the embedded migration idempotently on startup; all
// queries are parameterized.
type Store struct {
	pool *pgxpool.Pool
	// now is an injectable clock retained for time-dependent control-plane
	// operations; the limits lookup is billing-blind and no longer reads it.
	now func() time.Time
}

// New opens a pool against dsn, verifies connectivity, and applies the core
// migrations idempotently, followed by any sources registered via
// WithExtraMigrations. The caller owns the returned Store and must Close it.
func New(ctx context.Context, dsn string, opts ...Option) (*Store, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("control.New: pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("control.New: ping: %w", err)
	}
	if err := applyMigrations(ctx, pool, o.extraMigrations); err != nil {
		pool.Close()
		return nil, fmt.Errorf("control.New: migrate: %w", err)
	}
	return &Store{pool: pool, now: time.Now}, nil
}

// Close releases the underlying pool.
func (s *Store) Close() {
	s.pool.Close()
}

// Pool exposes the underlying pool, used by main to build a Resolver and to
// close the pool during shutdown.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// execer is the Exec subset applyMigrations needs, so migration ordering is
// unit-testable against a recording fake without a live Postgres.
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// applyMigrations runs the embedded core schema first, then each extra source in
// registration order. Every source's files apply in lexical filename order and
// must be idempotent (CREATE ... IF NOT EXISTS, INSERT ... ON CONFLICT), so
// applying them on every startup is safe.
func applyMigrations(ctx context.Context, db execer, extra []MigrationSource) error {
	sources := make([]MigrationSource, 0, 1+len(extra))
	sources = append(sources, MigrationSource{FS: migrationsFS, Dir: "migrations/postgres"})
	sources = append(sources, extra...)
	for _, src := range sources {
		if err := applyMigrationSource(ctx, db, src); err != nil {
			return err
		}
	}
	return nil
}

// applyMigrationSource applies one source's .sql files in lexical order.
func applyMigrationSource(ctx context.Context, db execer, src MigrationSource) error {
	entries, err := fs.ReadDir(src.FS, src.Dir)
	if err != nil {
		return fmt.Errorf("read migrations %s: %w", src.Dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		sqlBytes, err := fs.ReadFile(src.FS, src.Dir+"/"+name)
		if err != nil {
			return fmt.Errorf("read %s/%s: %w", src.Dir, name, err)
		}
		if _, err := db.Exec(ctx, string(sqlBytes)); err != nil {
			return fmt.Errorf("apply %s/%s: %w", src.Dir, name, err)
		}
	}
	return nil
}

// hashKey computes the sha256 digest of an ingest key. Keys are compared and
// stored as this digest, never as plaintext (CONTEXT Ingest key).
func hashKey(key string) []byte {
	sum := sha256.Sum256([]byte(key))
	return sum[:]
}

// EnsureDevKey creates (or reuses) an org named orgName and attaches an ingest
// key hashing to key, if not already present. It is a dev-seed convenience so
// local dev keeps working against a real control plane. It is
// idempotent: a re-run with the same key is a no-op that returns the tenant.
func (s *Store) EnsureDevKey(ctx context.Context, key, orgName string) (tenant string, err error) {
	return ensureDevKey(ctx, s.pool, key, orgName)
}

// ensureDevKey holds the seeding logic against a querier so it is testable
// without a live Postgres.
func ensureDevKey(ctx context.Context, q querier, key, orgName string) (string, error) {
	hash := hashKey(key)

	var orgID string
	err := q.QueryRow(ctx,
		`SELECT org_id::text FROM ingest_keys WHERE key_hash = $1`, hash,
	).Scan(&orgID)
	if err == nil {
		return orgID, nil
	}
	if err != pgx.ErrNoRows {
		return "", fmt.Errorf("control.EnsureDevKey: lookup: %w", err)
	}

	// Reuse an org with this name if present, else create one.
	err = q.QueryRow(ctx, `SELECT id::text FROM orgs WHERE name = $1 LIMIT 1`, orgName).Scan(&orgID)
	if err == pgx.ErrNoRows {
		if err = q.QueryRow(ctx,
			`INSERT INTO orgs (name) VALUES ($1) RETURNING id::text`, orgName,
		).Scan(&orgID); err != nil {
			return "", fmt.Errorf("control.EnsureDevKey: create org: %w", err)
		}
	} else if err != nil {
		return "", fmt.Errorf("control.EnsureDevKey: find org: %w", err)
	}

	if _, err := q.Exec(ctx,
		`INSERT INTO ingest_keys (org_id, label, key_hash) VALUES ($1, $2, $3)`,
		orgID, "dev", hash,
	); err != nil {
		return "", fmt.Errorf("control.EnsureDevKey: insert key: %w", err)
	}
	return orgID, nil
}

// IssueKey creates a new active ingest key for orgID and returns its plaintext
// secret exactly once — only sha256(secret) and a display last-4 are stored. The
// caller wraps the secret with the deployment origin via token.Encode before
// showing it to the user (control does not know the server's public URL).
func (s *Store) IssueKey(ctx context.Context, orgID, label string) (secret string, err error) {
	return issueKey(ctx, s.pool, orgID, label)
}

// issueKey holds the generation + insert against a querier so it is testable
// without a live Postgres.
func issueKey(ctx context.Context, q querier, orgID, label string) (string, error) {
	secret, err := token.NewSecret()
	if err != nil {
		return "", fmt.Errorf("control.IssueKey: secret: %w", err)
	}
	last4 := secret
	if len(last4) > 4 {
		last4 = last4[len(last4)-4:]
	}
	if _, err := q.Exec(ctx,
		`INSERT INTO ingest_keys (org_id, label, key_hash, last4) VALUES ($1, $2, $3, $4)`,
		orgID, label, hashKey(secret), last4,
	); err != nil {
		return "", fmt.Errorf("control.IssueKey: insert: %w", err)
	}
	return secret, nil
}

// RevokeKey marks an active key revoked. It returns ErrKeyNotFound if no active
// key matches (already revoked, wrong org, or unknown id).
func (s *Store) RevokeKey(ctx context.Context, orgID, keyID string) error {
	return revokeKey(ctx, s.pool, orgID, keyID)
}

func revokeKey(ctx context.Context, q querier, orgID, keyID string) error {
	return execExpectOne(ctx, q, "control.RevokeKey", ErrKeyNotFound,
		`UPDATE ingest_keys SET revoked_at = now() WHERE id = $1 AND org_id = $2 AND revoked_at IS NULL`,
		keyID, orgID)
}

// execExpectOne runs a single-row-affecting write and maps "no row matched" to
// notFound, wrapping a genuine database error with label. It is the shared shape of
// the revoke-style operations (revoke key, revoke invite).
func execExpectOne(ctx context.Context, q querier, label string, notFound error, sql string, args ...any) error {
	tag, err := q.Exec(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	if tag.RowsAffected() == 0 {
		return notFound
	}
	return nil
}

// queryList runs a parameterized list query and drains it through scan, the shared
// shape of the list endpoints (keys, members, invites): each caller supplies only
// its SQL, args, and per-row Scan. Errors are wrapped with label.
func queryList[T any](ctx context.Context, q rowsQuerier, label, sql string, args []any, scan func(pgx.Rows) (T, error)) ([]T, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: query: %w", label, err)
	}
	return collectRows(rows, label, scan)
}

// collectRows drains a result set through scan into a slice, wrapping scan/rows
// errors with label.
func collectRows[T any](rows pgx.Rows, label string, scan func(pgx.Rows) (T, error)) ([]T, error) {
	defer rows.Close()
	var out []T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("%s: scan: %w", label, err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: rows: %w", label, err)
	}
	return out, nil
}

// ListKeys returns an org's ingest keys (active and revoked), newest first. The
// secret is never returned — only the display last-4 and lifecycle timestamps.
//nolint:dupl // parallel list endpoint; the shared shape is already factored into queryList, only the SQL + Scan differ.
func (s *Store) ListKeys(ctx context.Context, orgID string) ([]KeyInfo, error) {
	return queryList(ctx, s.pool, "control.ListKeys",
		`SELECT id::text, label, last4, created_at, revoked_at
		   FROM ingest_keys WHERE org_id = $1 ORDER BY created_at DESC`, []any{orgID},
		func(r pgx.Rows) (KeyInfo, error) {
			var k KeyInfo
			err := r.Scan(&k.ID, &k.Label, &k.Last4, &k.CreatedAt, &k.RevokedAt)
			return k, err
		})
}

// Limits resolves tenant's plan-limits budget: it reads the org's plan and
// returns the plan_limits row for that plan. It resolves only the plan budget —
// any further per-tenant policy lives in an injectable LimitProvider a composing
// build supplies, not here. It falls back to guardrail.DefaultLimits when the org
// or its plan row is missing, so a misconfigured plan never fails ingest open on
// the limit budget.
func (s *Store) Limits(ctx context.Context, tenant string) (guardrail.Limits, error) {
	return queryLimits(ctx, s.pool, tenant)
}

// queryLimits is the parameterized, billing-blind limits lookup against a
// querier. It joins the org's plan to its plan_limits budget and returns exactly
// that budget; a missing org/plan row falls back to DefaultLimits.
func queryLimits(ctx context.Context, q querier, tenant string) (guardrail.Limits, error) {
	var (
		plan string
		l    guardrail.Limits
	)
	err := q.QueryRow(ctx, `
		SELECT o.plan, pl.max_rps, pl.burst, pl.cardinality_cap, pl.max_payload_bytes, pl.retention_days
		FROM orgs o
		JOIN plan_limits pl ON pl.plan = o.plan
		WHERE o.id = $1`, tenant,
	).Scan(&plan, &l.MaxRPS, &l.Burst, &l.CardinalityCap, &l.MaxPayloadBytes, &l.RetentionDays)
	if err == pgx.ErrNoRows {
		return guardrail.DefaultLimits(), nil
	}
	if err != nil {
		return guardrail.Limits{}, fmt.Errorf("control.Limits: %w", err)
	}
	return l, nil
}
