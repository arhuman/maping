---
status: accepted
---

# One verified email maps to one org

First-login resolution keyed solely on the OIDC `oidc_subject` (`github:<id>`,
`google:<sub>`), and each miss created a fresh org-of-one. Signing in through a
second provider therefore produced a second `oidc_subject`, a second org, and a
second plan for the same human — observed in production as one person holding both a
`pro/active` and a `free/none` org. Plans are per-org, so this split a paying customer
across two tenants. This ADR records the fix.

## Decision

- **Link a new identity into an org that already belongs to its verified email.** In
  `upsertMemberFromOIDC`, when no member matches the `oidc_subject` (first login for
  this identity), we look up an org already owned by the login's email before minting
  a new one. On a match we insert a new admin member carrying the new
  `oidc_subject` into that org and report `isNew=false` (an existing org, so no
  reveal-once key interstitial). Only with no match do we create the org-of-one, as
  before (`isNew=true`). The target org is chosen deterministically: a paid org over a
  free one, then the oldest, so a person converges on their revenue-bearing tenant.
- **Only ever link on a provider-verified email.** Linking by email is safe precisely
  because the email is provider-verified: `googleIdentity` already required
  `email_verified`, and `githubIdentity` is tightened here to accept only a
  `primary && verified` address from `/user/emails` — the previous fallback to the
  (possibly unverified) public profile email is removed, and a failed emails lookup is
  now a hard error rather than a silent downgrade. An unverified address must never
  reach a `members` row, or a stranger could claim another person's org by registering
  their email at a second provider.
- **A linked identity is a second `members` row, not a merged one.** The same human
  ends up with two member rows (one per provider) in one org. This keeps the change to
  a single query branch and no schema change. It has one cost: each identity counts as
  a seat.

## Consequences

- The duplicate-per-provider path is closed going forward. Orgs that were *already*
  split before this change are not healed retroactively (both identities already have
  member rows, so neither hits the link path); they are merged once, by hand, via
  `maping-enterprise/ops/merge-dup-org.sql`.
- A person who links a second provider consumes a second seat in their org. If this
  proves annoying at a low free-tier seat cap, the clean fix is a `member_identities`
  table (one member, many OIDC identities) so an identity is not a seat; that is
  deliberately deferred, not built here.
- A GitHub account with no verified primary email can no longer sign in at all (it
  previously could, via the public profile email). This is the intended safety
  posture now that email drives account linking.
