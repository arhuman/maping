---
status: superseded by 0022-relicense-server-mit
---

# Open-core: MIT client, BSL server

> **Superseded by [ADR-0022](0022-relicense-server-mit.md).** The server was relicensed
> from BSL 1.1 to MIT; the whole repository is now MIT. The reasoning below is kept for
> the historical record. The *code-absence invariant* it introduced still holds — see 0022.

The mAPI-ng **client library is open source under MIT**. The **server**
(collector, ClickHouse schema, rollups, dashboard, control plane) is
**source-available under the Business Source License (BSL)**. This is the open-core
model (Sentry/PostHog/Axiom shape).

## Why

Teams will not inject a closed-source binary into their production API hot path — an
auditable, permissively-licensed client is effectively a product requirement, and it
doubles as the primary trust/adoption surface. **MIT** is chosen for maximum
permissiveness and simplicity, minimizing adoption friction. Trade-off accepted: MIT
carries no explicit patent grant (Apache-2.0 would); we judge the simplicity and
familiarity of MIT worth more than that protection for a lightweight instrumentation
library. The server is where the commercial value sits (hosting, retention, seats,
future alerting); BSL keeps the source visible and self-hostable for non-competing use
while preventing a competitor from reselling it as a rival hosted service.

## Consequences

Because the client is public, the **wire protocol (protobuf + Connect/gRPC) becomes a
published, stable contract** that third parties may build against — reinforcing the
backward-compatible schema-evolution discipline of ADR-0002. BSL typically converts
to an open license after a set term; that clock and the exact BSL "additional use
grant" are to be defined before publishing the server.
