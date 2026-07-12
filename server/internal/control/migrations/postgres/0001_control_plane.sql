-- 0001_control_plane.sql — mAPI-ng control plane (Postgres).
--
-- The small, transactional relational store of tenants (orgs), ingest keys,
-- members, and plan limits (CONTEXT Control plane). Ingest validates keys
-- against it (cached) and resolves the tenant; the data plane (ClickHouse) is
-- row-level multitenant and never joins here.
--
-- Every statement is idempotent (IF NOT EXISTS) so the embedded migration can be
-- applied unconditionally on startup. gen_random_uuid() needs pgcrypto.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- orgs: one tenant = one organization (CONTEXT Tenant). A solo user is an org
-- of one.
CREATE TABLE IF NOT EXISTS orgs (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL,
    plan       text NOT NULL DEFAULT 'free',
    created_at timestamptz NOT NULL DEFAULT now()
);

-- ingest_keys: many labeled, independently revocable keys per org. Keys are
-- stored hashed (sha256), never in plaintext (CONTEXT Ingest key; review #11).
-- A NULL revoked_at means the key is active.
CREATE TABLE IF NOT EXISTS ingest_keys (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id     uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    label      text NOT NULL,
    key_hash   bytea NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz
);

-- members: humans in an org, authenticated via OIDC only (M4). Schema only for
-- now; oidc_subject is populated at login time.
CREATE TABLE IF NOT EXISTS members (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id       uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    email        text NOT NULL,
    role         text NOT NULL CHECK (role IN ('admin', 'member')),
    oidc_subject text,
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- plan_limits: per-plan guardrail budget. Resolved per tenant via orgs.plan and
-- enforced by the guardrail layer / ClickHouse TTL (retention_days).
CREATE TABLE IF NOT EXISTS plan_limits (
    plan              text PRIMARY KEY,
    max_rps           double precision NOT NULL,
    burst             int NOT NULL,
    cardinality_cap   int NOT NULL,
    max_payload_bytes bigint NOT NULL,
    retention_days    int NOT NULL
);

-- Seed the free plan. ON CONFLICT DO NOTHING keeps the migration idempotent and
-- non-destructive of any operator-tuned row.
INSERT INTO plan_limits (plan, max_rps, burst, cardinality_cap, max_payload_bytes, retention_days)
VALUES ('free', 100, 200, 10000, 4194304, 30)
ON CONFLICT (plan) DO NOTHING;
