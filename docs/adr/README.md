# Architecture Decision Records

Design decisions for mAPI-ng, one file per decision, numbered sequentially. Superseded
records keep their number and are marked here rather than deleted.

| ADR | Title |
|---|---|
| [0001](0001-ddsketch-for-latency.md) | DDSketch for latency aggregation |
| [0002](0002-connect-client-grpc-server.md) | Connect client / gRPC server |
| [0003](0003-clickhouse-storage.md) | ClickHouse for storage |
| [0004](0004-open-core-licensing.md) | Open-core: MIT client, BSL server (superseded by 0022) |
| [0005](0005-ingest-direct-then-queue.md) | Direct batched ClickHouse writes for v1, durable queue later |
| [0006](0006-dashboard-server-rendered-htmx-uplot.md) | Dashboard: server-rendered Go + htmx + uPlot (superseded by 0008) |
| [0007](0007-dashboard-auth-oidc-session-cookies.md) | Dashboard auth: OIDC, stateless session cookies |
| [0008](0008-dashboard-js-budget-csp.md) | Dashboard JS budget and Content-Security-Policy |
| [0009](0009-setup-form-csrf-synchronizer-token.md) | Setup form CSRF: stateless HMAC synchronizer token |
| [0010](0010-tenant-scoped-queries.md) | Tenant-scoped data-plane access |
| [0011](0011-ci-quality-gate.md) | CI quality gate: Makefile targets on push and PR |
| [0012](0012-aggregating-schema-instance-sort-key.md) | Summaries aggregate-state columns and an instance/method sort key |
| [0013](0013-deploy-version-dimension.md) | Deploy identity (version / env / region) as a stored dimension |
| [0014](0014-exemplars-and-max-latency.md) | Exemplars: bounded request breadcrumbs from an aggregate to a trace |
| [0015](0015-use-gauges-instance-windows.md) | USE gauges: per-instance saturation as a separate stream |
| [0016](0016-composition-seams.md) | Composition seams: out-of-tree features via app.Run options |
| [0017](0017-tier1-memstats-fields.md) | Additive MemStats fields on the instance-window stream |
| [0018](0018-post-gc-heap-and-true-rss.md) | Post-GC heap baseline and true RSS on the instance-window stream |
| [0019](0019-leak-vs-burst-memory-verdict.md) | Leak-vs-burst memory verdict on the endpoint detail page |
| [0020](0020-congestion-fd-in-flight.md) | File-descriptor and in-flight congestion gauges on the instance-window stream |
| [0021](0021-diagnosis-engine.md) | Diagnosis engine on the endpoint detail page |
| [0022](0022-relicense-server-mit.md) | Relicense the server to MIT (supersedes 0004) |
| [0023](0023-in-app-documentation.md) | In-app documentation |
