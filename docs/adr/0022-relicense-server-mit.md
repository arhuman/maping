---
status: accepted
supersedes: 0004-open-core-licensing
---

# Relicense the server to MIT

The **server** (collector, ClickHouse schema, rollups, dashboard, control plane)
moves from the **Business Source License 1.1** to the **MIT License**. Every other
module (`proto`, `client` and its adapters, `example`) was already MIT, so the
entire public `maping` repository is now uniformly MIT-licensed. This supersedes
ADR-0004, which placed the server under BSL for non-competing use only.

## Why

BSL bought one thing: a clause preventing a third party from reselling the server
as a competing hosted service, converting to Apache 2.0 on a Change Date (2030-07-09).
For an early-stage, single-maintainer project that protection is worth less than what
it costs: BSL is source-available, not open source, so it dampens the adoption and
trust that are the actual scarce resource at this stage. It complicates the story on
every surface (per-module license map, marketing caveats, contributor terms, CI gates)
and the "Competing Service" line is hard to define and harder to enforce alone.

MIT removes that friction entirely: the core becomes unambiguously open source,
maximally adoptable, trivial to reason about. We accept the known trade-off already
accepted for the client in ADR-0004 — MIT carries no explicit patent grant (Apache-2.0
would) — judging simplicity and familiarity worth more than that protection.

The commercial moat moves from **license** to **execution**: the value we sell is the
hosted service (zero-ops, retention, SLA) and the enterprise business layer (SSO, seats,
account management), not a legal restriction on self-hosting. Anyone may now host or
resell the core; we compete on running it well, not on forbidding it.

## Consequences

- **Irreversible.** MIT rights, once published, cannot be revoked. A competitor may
  legally host and resell the core. This decision is made with that understood.
- The **open-core *architecture* is unchanged**: the enterprise binary stays a separate,
  proprietary module composed over the core's seams (mapi-ng ADR-0016 / enterprise
  ADR-0001). "Open core" now describes the code boundary, not a license split.
- **The code-absence invariant survives and stays enforced.** The commercial
  hosted-service vocabulary must still never appear in the public tree; the CI
  `public-tree` gate remains (the exact term list lives in that CI job). Relicensing the
  server does not license *enterprise* code — that code simply is not here.
- The BSL machinery is dropped: no Change Date, no Change License, no "Additional Use
  Grant", no per-module license map. The root `LICENSE` becomes a plain MIT license and
  each module keeps an MIT `LICENSE` for tooling.
- Marketing is repositioned from "source-available / non-competing" to "fully open
  source (MIT); the hosted service is the product." The published wire protocol
  (protobuf + Connect/gRPC) remains a stable third-party contract, as under ADR-0004.
