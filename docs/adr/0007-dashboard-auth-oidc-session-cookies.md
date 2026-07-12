---
status: accepted
---

# Dashboard auth: OIDC, stateless session cookies

Dashboard authentication uses OIDC via `golang.org/x/oauth2` (GitHub and/or Google),
with identity resolved through the provider userinfo endpoint. Sessions are stored as
HMAC-SHA256-signed stateless cookies; there is no server-side session store.

## Context

The dashboard is multi-tenant (../context.md): each member must see only their own org's
data. The control plane (Postgres) holds the member and org records. Auth is only wired
when a control plane is present; without Postgres the server runs in constant-tenant dev
mode with no login.

## Decision

**OIDC via `golang.org/x/oauth2`.** GitHub and Google are the two supported providers.
Each is independently optional. Identity is verified by fetching the provider's userinfo
endpoint after the token exchange (no passwords stored, no SAML in v1). On first login,
`UpsertMemberFromOIDC` creates an org-of-one with an admin member inside a transaction;
subsequent logins resolve the existing member.

**Stateless HMAC-signed session cookies.** The session payload (`orgID|memberID|role|exp`)
is base64-encoded and signed with HMAC-SHA256 using a key from `MAPING_SESSION_KEY`
(>= 32 bytes; **required for an https deployment** — the server refuses to start without
it rather than mint an ephemeral key that would drop every session on restart; local http
dev generates an ephemeral key with a startup warning). No server-side
session table. Cookie expiry is 7 days.

**Three startup modes** (selected automatically, no manual configuration):

- No `MAPING_POSTGRES_DSN`: auth off, constant dev tenant, no login routes.
- `MAPING_POSTGRES_DSN` set, no OIDC credentials: dev-login only (a button that starts
  a session as the seeded dev-org admin). Intended for local testing with a real control
  plane but without a registered OAuth app.
- `MAPING_POSTGRES_DSN` + at least one provider's credentials: real OIDC login. Dev-login
  is disabled when any real provider is configured so a production deployment never exposes
  the bypass.

**OAuth CSRF protection.** The authorization state is stored in a short-lived (10 min)
HMAC-signed HttpOnly cookie, verified on callback before the token exchange.

## Why stateless cookies

A server-side session store (Redis, Postgres table) is the standard approach for distributed
deployments. For v1 it adds an operational dependency and a write on every login. Stateless
cookies carry all the identity the dashboard needs (org id, member id, role, expiry) and
require no extra infrastructure. The trade-off is that sessions cannot be revoked server-side
before expiry, which is acceptable for v1 (7-day expiry; org membership is enforced at login).

## Consequences

Session revocation (e.g. removing a member) takes effect at the next cookie expiry, not
immediately. A server-side session table would be needed to close this gap in v2. The
`MAPING_SESSION_KEY` must be the same across all instances of a multi-instance deployment;
with an ephemeral key, sessions do not survive a process restart.
