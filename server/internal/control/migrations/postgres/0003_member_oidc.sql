-- 0003_member_oidc.sql — index members by their OIDC subject.
--
-- Members authenticate via OIDC only (GitHub/Google, no passwords — CONTEXT
-- Member / roles). On login the auth layer resolves an existing member by its
-- provider-prefixed oidc_subject (e.g. 'github:12345', 'google:sub'); on first
-- login it creates an org-of-one and an admin member. A unique index makes the
-- subject lookup fast and guarantees one member per external identity.
--
-- Idempotent (IF NOT EXISTS) so the embedded migration applies unconditionally
-- on startup. Partial (WHERE oidc_subject IS NOT NULL) because the schema allows
-- a NULL subject (members created before their first login) and NULLs must not
-- collide under the unique constraint.
CREATE UNIQUE INDEX IF NOT EXISTS members_oidc_subject_key
    ON members (oidc_subject)
    WHERE oidc_subject IS NOT NULL;
