package control

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ServiceOnboarding is one connected (service, instance) of a tenant and when it
// was first seen, feeding the dashboard's live onboarding panel (CONTEXT
// Handshake). HandshakeAt is first_seen so the panel can show how long a source
// has been connected without yet sending a Summary.
type ServiceOnboarding struct {
	Service     string
	Instance    string
	HandshakeAt time.Time
}

// RecordHandshake upserts the handshake for (tenant, service, instance),
// refreshing last_seen and sdk_version and preserving first_seen. It is called
// from the ingest Register path after successful auth (via the HandshakeRecorder
// adapter in main), so the dashboard's step 2 (service connected) reflects
// reality as soon as a recorder pings. Errors are the caller's to log-and-
// continue: a control-plane write must never fail the handshake itself (the
// ping's job is proving auth + connectivity, not persistence).
func (s *Store) RecordHandshake(ctx context.Context, tenant, service, instance, sdkVersion string) error {
	return recordHandshake(ctx, s.pool, tenant, service, instance, sdkVersion)
}

// recordHandshake holds the upsert against a querier so it is unit-testable
// against the fake querier without a live Postgres.
func recordHandshake(ctx context.Context, q querier, tenant, service, instance, sdkVersion string) error {
	if _, err := q.Exec(ctx, `
		INSERT INTO handshakes (org_id, service, instance, sdk_version, first_seen, last_seen)
		VALUES ($1, $2, $3, $4, now(), now())
		ON CONFLICT (org_id, service, instance)
		DO UPDATE SET last_seen = now(), sdk_version = EXCLUDED.sdk_version`,
		tenant, service, instance, sdkVersion,
	); err != nil {
		return fmt.Errorf("control.RecordHandshake: %w", err)
	}
	return nil
}

// OnboardingState returns every connected (service, instance) of tenant ordered
// by first_seen, so the dashboard can render the live onboarding progress. An
// empty slice means no source has handshaked yet (still on step 1: key valid).
func (s *Store) OnboardingState(ctx context.Context, tenant string) ([]ServiceOnboarding, error) {
	return onboardingState(ctx, s.pool, tenant)
}

// rowsQuerier is the read subset OnboardingState needs. pgxpool.Pool satisfies
// it; the fake in store_test.go can too, keeping the query unit-testable.
type rowsQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// onboardingState holds the parameterized list query against a rowsQuerier.
func onboardingState(ctx context.Context, q rowsQuerier, tenant string) ([]ServiceOnboarding, error) {
	rows, err := q.Query(ctx, `
		SELECT service, instance, first_seen
		FROM handshakes
		WHERE org_id = $1
		ORDER BY first_seen`, tenant)
	if err != nil {
		return nil, fmt.Errorf("control.OnboardingState: query: %w", err)
	}
	defer rows.Close()

	var out []ServiceOnboarding
	for rows.Next() {
		var o ServiceOnboarding
		if err := rows.Scan(&o.Service, &o.Instance, &o.HandshakeAt); err != nil {
			return nil, fmt.Errorf("control.OnboardingState: scan: %w", err)
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("control.OnboardingState: rows: %w", err)
	}
	return out, nil
}
