package control

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// txBeginner starts a transaction returning a pgx.Tx. *pgxpool.Pool satisfies
// it; a fake in member_test.go can too, so the two-statement member+org create
// stays unit-testable without a live Postgres. It is the write-side counterpart
// of the read-only querier interface.
type txBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// UpsertMemberFromOIDC resolves the member behind an OIDC identity, creating an
// org-of-one on first login. oidcSubject is the provider-prefixed stable id
// (e.g. "github:12345"); email is the verified primary email.
//
// On first login (no member with this oidcSubject) it creates, atomically in one
// transaction, an org (plan 'free') and an admin member in it — a solo user is
// an org of one and the first user is that org's admin (CONTEXT Member / roles,
// Tenant). On a repeat login it resolves the existing member and returns its
// org/role unchanged. Returns the org id (the tenant the dashboard renders), the
// member id, and the member's role.
func (s *Store) UpsertMemberFromOIDC(ctx context.Context, oidcSubject, email string) (orgID, memberID, role string, isNew bool, err error) {
	return upsertMemberFromOIDC(ctx, s.pool, oidcSubject, email)
}

// upsertMemberFromOIDC holds the resolution/creation logic against a txBeginner
// so it is testable against a fake without a live Postgres. The lookup runs
// first (outside the transaction, read-only); only the first-login create path
// opens a transaction so org+member creation is atomic.
func upsertMemberFromOIDC(ctx context.Context, tb txBeginner, oidcSubject, email string) (orgID, memberID, role string, isNew bool, err error) {
	tx, err := tb.Begin(ctx)
	if err != nil {
		return "", "", "", false, fmt.Errorf("control.UpsertMemberFromOIDC: begin: %w", err)
	}
	// Rollback is a no-op after Commit; deferring it covers every error return.
	defer func() { _ = tx.Rollback(ctx) }()

	// Existing member for this identity?
	err = tx.QueryRow(ctx,
		`SELECT id::text, org_id::text, role FROM members WHERE oidc_subject = $1`, oidcSubject,
	).Scan(&memberID, &orgID, &role)
	if err == nil {
		if err = tx.Commit(ctx); err != nil {
			return "", "", "", false, fmt.Errorf("control.UpsertMemberFromOIDC: commit lookup: %w", err)
		}
		return orgID, memberID, role, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", "", "", false, fmt.Errorf("control.UpsertMemberFromOIDC: lookup: %w", err)
	}

	// First login: create an org-of-one and its admin member atomically. Name
	// the org after the email so the dashboard has a human label.
	if err = tx.QueryRow(ctx,
		`INSERT INTO orgs (name, plan) VALUES ($1, 'free') RETURNING id::text`, email,
	).Scan(&orgID); err != nil {
		return "", "", "", false, fmt.Errorf("control.UpsertMemberFromOIDC: create org: %w", err)
	}
	role = "admin"
	if err = tx.QueryRow(ctx,
		`INSERT INTO members (org_id, email, role, oidc_subject)
		 VALUES ($1, $2, 'admin', $3) RETURNING id::text`,
		orgID, email, oidcSubject,
	).Scan(&memberID); err != nil {
		return "", "", "", false, fmt.Errorf("control.UpsertMemberFromOIDC: create member: %w", err)
	}

	if err = tx.Commit(ctx); err != nil {
		return "", "", "", false, fmt.Errorf("control.UpsertMemberFromOIDC: commit create: %w", err)
	}
	return orgID, memberID, role, true, nil
}

// DevOrgAdmin ensures an admin member exists in the seeded dev org and returns
// it, for the dev-login auth mode (control plane present but no OIDC provider
// configured). It resolves the dev org by name (created by EnsureDevKey) and
// upserts a synthetic admin member keyed on a fixed dev oidc_subject, so repeat
// dev-logins reuse the same member. Idempotent.
func (s *Store) DevOrgAdmin(ctx context.Context, devOrgName string) (orgID, memberID string, err error) {
	return devOrgAdmin(ctx, s.pool, devOrgName)
}

// devDBSubject is the fixed oidc_subject stamped on the dev-login member, kept
// distinct from any real provider prefix so it can never collide with a
// github:/google: identity.
const devDBSubject = "dev:admin"

// devOrgAdmin holds the dev-admin ensure logic against a querier (read + write,
// no multi-statement atomicity needed — a re-run just re-finds the same row).
func devOrgAdmin(ctx context.Context, q querier, devOrgName string) (orgID, memberID string, err error) {
	err = q.QueryRow(ctx, `SELECT id::text FROM orgs WHERE name = $1 LIMIT 1`, devOrgName).Scan(&orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", fmt.Errorf("control.DevOrgAdmin: dev org %q not found (seed it first)", devOrgName)
	}
	if err != nil {
		return "", "", fmt.Errorf("control.DevOrgAdmin: find org: %w", err)
	}

	// Reuse the dev admin member if it already exists.
	err = q.QueryRow(ctx,
		`SELECT id::text FROM members WHERE oidc_subject = $1`, devDBSubject,
	).Scan(&memberID)
	if err == nil {
		return orgID, memberID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", "", fmt.Errorf("control.DevOrgAdmin: find member: %w", err)
	}

	if err = q.QueryRow(ctx,
		`INSERT INTO members (org_id, email, role, oidc_subject)
		 VALUES ($1, 'dev@maping.local', 'admin', $2) RETURNING id::text`,
		orgID, devDBSubject,
	).Scan(&memberID); err != nil {
		return "", "", fmt.Errorf("control.DevOrgAdmin: create member: %w", err)
	}
	return orgID, memberID, nil
}
