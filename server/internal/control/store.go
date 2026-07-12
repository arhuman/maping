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
	"sort"
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
}

// New opens a pool against dsn, verifies connectivity, and applies the embedded
// migration idempotently. The caller owns the returned Store and must Close it.
func New(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("control.New: pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("control.New: ping: %w", err)
	}
	if err := applyMigrations(ctx, pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("control.New: migrate: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close releases the underlying pool.
func (s *Store) Close() {
	s.pool.Close()
}

// Pool exposes the underlying pool, used by main to build a Resolver and to
// close the pool during shutdown.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// applyMigrations runs every embedded .sql file in lexical order. Files are
// idempotent (CREATE ... IF NOT EXISTS, INSERT ... ON CONFLICT), so applying
// them on every startup is safe.
func applyMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	entries, err := migrationsFS.ReadDir("migrations/postgres")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		sqlBytes, err := migrationsFS.ReadFile("migrations/postgres/" + name)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		if _, err := pool.Exec(ctx, string(sqlBytes)); err != nil {
			return fmt.Errorf("apply %s: %w", name, err)
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
	tag, err := q.Exec(ctx,
		`UPDATE ingest_keys SET revoked_at = now() WHERE id = $1 AND org_id = $2 AND revoked_at IS NULL`,
		keyID, orgID,
	)
	if err != nil {
		return fmt.Errorf("control.RevokeKey: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrKeyNotFound
	}
	return nil
}

// ListKeys returns an org's ingest keys (active and revoked), newest first. The
// secret is never returned — only the display last-4 and lifecycle timestamps.
func (s *Store) ListKeys(ctx context.Context, orgID string) ([]KeyInfo, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id::text, label, last4, created_at, revoked_at
		   FROM ingest_keys WHERE org_id = $1 ORDER BY created_at DESC`, orgID)
	if err != nil {
		return nil, fmt.Errorf("control.ListKeys: query: %w", err)
	}
	defer rows.Close()
	var out []KeyInfo
	for rows.Next() {
		var k KeyInfo
		if err := rows.Scan(&k.ID, &k.Label, &k.Last4, &k.CreatedAt, &k.RevokedAt); err != nil {
			return nil, fmt.Errorf("control.ListKeys: scan: %w", err)
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("control.ListKeys: rows: %w", err)
	}
	return out, nil
}

// Limits resolves tenant's plan limits: orgs.plan -> plan_limits row. It falls
// back to guardrail.DefaultLimits when the org or its plan row is missing, so a
// misconfigured plan never fails ingest open on the limit budget.
func (s *Store) Limits(ctx context.Context, tenant string) (guardrail.Limits, error) {
	return queryLimits(ctx, s.pool, tenant)
}

// queryLimits is the parameterized limits lookup against a querier.
func queryLimits(ctx context.Context, q querier, tenant string) (guardrail.Limits, error) {
	var l guardrail.Limits
	err := q.QueryRow(ctx, `
		SELECT pl.max_rps, pl.burst, pl.cardinality_cap, pl.max_payload_bytes, pl.retention_days
		FROM orgs o
		JOIN plan_limits pl ON pl.plan = o.plan
		WHERE o.id = $1`, tenant,
	).Scan(&l.MaxRPS, &l.Burst, &l.CardinalityCap, &l.MaxPayloadBytes, &l.RetentionDays)
	if err == pgx.ErrNoRows {
		return guardrail.DefaultLimits(), nil
	}
	if err != nil {
		return guardrail.Limits{}, fmt.Errorf("control.Limits: %w", err)
	}
	return l, nil
}
