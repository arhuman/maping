# Security and data flow

## Data flow

```
Your service (client library)
  -> in-process aggregation (DDSketch + counters, per flush window)
  -> Connect/gRPC upload (protobuf, zstd-compressed, TLS in production)
  -> ingest handler (authenticate key, resolve tenant, apply guardrails)
  -> ClickHouse (data plane: Summaries, rolled up over time)

Dashboard (server-rendered)
  -> OIDC login (GitHub / Google) -> Postgres (control plane: tenants, members, keys)
  -> tenant-scoped ClickHouse queries -> HTML response
```

Nothing is written to disk on the client side; aggregation lives in process
memory for the duration of one flush window before being shipped.

## Ingest key and tenant resolution

The only credential a client needs is the ingest key, delivered via
`MAPING_KEY`. It is sent as a bearer `Authorization` header on every
request. The server resolves the tenant from the key; the tenant is never
configured client-side. A tenant can hold several independently
revocable/rotatable labeled keys (for example one per environment), all
resolving to the same organization's data: keys are a rotation convenience,
not an isolation boundary between them.

## Handshake

On startup, a recorder with a resolved key sends a one-time registration
ping (service, instance, SDK version) before any metrics traffic. It proves
the key is valid and the collector is reachable, and drives the dashboard's
onboarding state. Setup problems (a bad key, clock skew, a frozen
cardinality cap, network or TLS failures) are logged at a rate-limited level
on the host and surfaced in the dashboard; metrics data itself still fails
open, but setup problems are not silent.

## Dashboard authentication

The dashboard has three startup modes, selected automatically from
configuration:

- No `MAPING_POSTGRES_DSN`: auth is off, a single constant dev tenant is
  used, and there are no login routes. This is the local/dev default.
- `MAPING_POSTGRES_DSN` set, no OIDC credentials: a dev-login button starts a
  session as the seeded dev-org admin, for testing against a real control
  plane without registering an OAuth app.
- `MAPING_POSTGRES_DSN` plus at least one OIDC provider's credentials: real
  login via GitHub and/or Google. No passwords are stored; identity is
  resolved through the provider's userinfo endpoint after token exchange.
  Dev-login is disabled whenever a real provider is configured.

Sessions are stateless, HMAC-SHA256-signed cookies keyed by
`MAPING_SESSION_KEY` (32 bytes minimum), with a 7-day expiry. There is no
server-side session store, so a production HTTPS deployment must set this
key explicitly and keep it stable across restarts and across every instance
of a multi-instance deployment; a plain HTTP dev deployment falls back to an
ephemeral key with a startup warning.

## CSRF and content security policy

State-changing dashboard actions (creating or revoking an ingest key) are
protected by a stateless HMAC synchronizer token bound to the caller's
organization, issued on the form page and verified before any mutation, in
addition to the `SameSite=Lax` session cookie. Every dashboard HTML response
carries a Content-Security-Policy restricting scripts to a single
self-hosted helper (`script-src 'self'`); the public documentation pages
(including this one) carry `script-src 'none'`, since they contain no
JavaScript at all.

## Tenant isolation

ClickHouse multitenancy is row-level: every row is stamped with the
server-resolved tenant, and there is no infrastructure wall between
tenants. Isolation is enforced in code: the read API only exposes queries
bound to a resolved tenant handle, so an un-scoped, cross-tenant query is
not something the query layer can express.

## Transport security

Client uploads use gRPC over HTTP/2. An `https://` endpoint uses TLS; an
`http://` endpoint (local/dev only) uses cleartext HTTP/2 (H2C). The
production docker-compose overlay publishes only the server's port; the
data plane and control plane stay on the internal network.
