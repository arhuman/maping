-- 0002_handshakes.sql — onboarding handshake registry (Postgres).
--
-- A Handshake is the one-time registration ping a recorder sends on startup with
-- a valid key (CONTEXT Handshake): it proves auth + connectivity before any
-- traffic and drives the dashboard's live 4-step onboarding state. This table is
-- the persistent record of "which service instances of a tenant have ever
-- connected", so the dashboard can show step 2 (service connected) and step 3
-- (waiting for first Summary) even across restarts. It is NOT metric data — the
-- Summaries store (ClickHouse) is separate.
--
-- Keyed by (org_id, service, instance) so a re-handshake upserts rather than
-- accumulating rows. first_seen is kept on conflict; last_seen and sdk_version
-- refresh on every ping. ON DELETE CASCADE ties a tenant's handshakes to its org
-- lifetime.
--
-- Idempotent (IF NOT EXISTS) so the embedded migration applies unconditionally.

CREATE TABLE IF NOT EXISTS handshakes (
    org_id      uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    service     text NOT NULL,
    instance    text NOT NULL,
    sdk_version text NOT NULL DEFAULT '',
    first_seen  timestamptz NOT NULL DEFAULT now(),
    last_seen   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, service, instance)
);
