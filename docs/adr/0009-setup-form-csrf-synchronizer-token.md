---
status: accepted
---

# Form CSRF for Setup key POSTs: stateless HMAC synchronizer token

The Setup page has two state-changing POST routes (`POST /setup/keys` and
`POST /setup/keys/{id}/revoke`). The session cookie is `SameSite=Lax`, which
already blocks cross-site form submissions in most browsers, but a stateless HMAC
synchronizer token bound to the caller's org is added as defense in depth.

## Context

The Setup keys panel lets a signed-in member create and revoke ingest keys for
their org. These mutations are the only form POSTs in the dashboard. The session
cookie (ADR-0007) establishes the org identity per request, but CSRF protection
provides a second layer: an attacker who can lure a logged-in user to a cross-site
page cannot read the authenticated form, so they cannot forge a valid submission.

## Decision

**Stateless HMAC synchronizer token.** The `csrf` type in `web/csrf.go` mints and
verifies tokens using HMAC-SHA256 keyed by `MAPING_SESSION_KEY` (the same key used
for session cookies). The token format is:

```
base64url(payload) + "." + base64url(HMAC-SHA256(payload))
```

where `payload` is the UTF-8 string `<orgID>|<base64url(16-byte-random-nonce)>`.
The org is embedded in the payload so each token is bound to exactly one org.

**Minted on `GET /setup`, embedded in every form.** `renderSetup` calls
`h.csrf.issue(tenant)` and passes the token as `CSRFToken` to the template. The
template embeds it as `<input type="hidden" name="csrf_token" value="...">` in the
create-key form and in each per-key revoke form.

**Verified before any mutation.** `checkCSRF` calls `csrf.verify(tenant, token)`,
which decodes the payload, checks the MAC with a constant-time `hmac.Equal`, and
then checks that the decoded payload starts with `tenant + "|"` and contains a
nonce after the separator. On failure it writes 403 and the handler returns without
performing any mutation. `r.FormValue` parses the POST body before this check.

**Nil-safe: no key, no panel, no routes.** When `KeyAdmin` is nil (dev or no
control plane), the `csrf` field is also nil, the keys panel is hidden, and
`serveCreateKey` / `serveRevokeKey` return 404 before `checkCSRF` is ever called.
`NewHandler` rejects a non-nil `KeyAdmin` paired with an empty `CSRFKey` at
construction time, so the invariant is enforced at startup.

**Synchronizer token, not double-submit cookie.** The token lives only in the
authenticated form HTML and is never set as a cookie. A cross-site page cannot read
the form, so it cannot obtain a valid token. A forged submission without the correct
MAC is rejected.

## Why a synchronizer token (not double-submit cookie)

The session cookie already establishes the org, so a signed token embedded in the
form is sufficient and simpler than a second cookie with its own `SameSite`
handling. Sharing the session-signing key is standard: both values are server-side
HMAC secrets and the shared key does not weaken either one. The pattern mirrors the
OAuth state-cookie HMAC approach from ADR-0007 for consistency.

The token is deliberately stateless (no server-side nonce store). CSRF protection
requires unforgeability and unpredictability, both provided by the HMAC and the
secrecy of the signing key. Replay within the same org is harmless for these
mutations; replay across orgs is blocked by the org binding in the MAC.

## Consequences

The token does not expire independently; its lifetime matches the session. This is
acceptable because CSRF security rests on MAC unforgeability and the inability of a
cross-site page to read the form, not on token rotation. A per-session-nonce
binding with server-side storage would provide replay protection within a session
but is deferred. The `MAPING_SESSION_KEY` rotation invalidates all outstanding CSRF
tokens simultaneously, which is correct behavior.
