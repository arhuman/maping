---
status: superseded by ADR-0008
---

# Dashboard: server-rendered Go + htmx + uPlot (superseded)

> **Superseded by [ADR-0008](0008-dashboard-js-budget-csp.md).** The server-rendered
> Go + `html/template` foundation still holds, but the **htmx + uPlot + CDN** parts of
> this decision were dropped. The shipped dashboard loads **no third-party JavaScript**:
> a strict `script-src 'self'` CSP (ADR-0008) rules out CDN scripts, charts are **inline
> server-rendered SVG** (`server/internal/web/chart.go`), the endpoint table sorts via
> plain query-param `<a>` links, and liveness is a `<meta http-equiv="refresh">`. The
> only script served is a self-hosted ~16-line `assets/copy.js`. The `/api/series` and
> `/api/histogram` JSON endpoints described below remain in the code but are not consumed
> by the current UI. The original decision is preserved below for historical context.

## Original decision (historical)

The mAPI-ng dashboard is served as server-rendered HTML from the Go server binary, using
`html/template` for autoescaping, htmx for partial-page updates, and uPlot for
time-series and latency histogram charts. Assets are loaded from CDNs.

## Context

The dashboard is fixed and non-configurable by design (../context.md): a 3-level RED view
(service overview, sortable endpoint table, endpoint detail with DDSketch histogram) plus
a 4-step onboarding panel. No custom panels, no query builder, no user dashboards in v1.
The deployment target is a single Go binary with ClickHouse as the only runtime dependency
for the data plane.

## Decision

Server-rendered HTML with htmx for interactive updates (sort, polling) and uPlot for
charts. JSON endpoints at `/api/series` and `/api/histogram` feed the chart data.
Templates live in the server binary (no separate static-file deployment step).

## Why not a SPA

A React/Vue SPA would require a separate build pipeline, a bundler, a distinct deployment
artifact, and a REST or GraphQL API contract to maintain. For a fixed, non-configurable
dashboard this is overhead without benefit: the interaction surface is narrow (three page
levels, one sort parameter, one polling interval), and the chart data feeds from two small
JSON endpoints that server-rendered templates can also consume directly.

htmx handles the narrow interactive surface (server-side sort, partial refreshes) without
client-side state management. uPlot is a minimal chart library with no framework dependency.
The resulting deployment is one Go binary with no front-end build step.

## Consequences

The dashboard cannot be decoupled from the server binary, and chart behavior is constrained
to what uPlot supports. Adding a configurable query builder or custom panels in v2 would
require revisiting this decision. The fixed-layout contract (../context.md) intentionally defers
those requirements.
