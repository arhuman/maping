---
status: accepted
---

# Operator usage seam: cross-tenant volumetry through the composition seam

The composing (enterprise) build owns an operator console that lists every account
and shows one account's volumetry (liveness, cardinality, recent requests, estimated
disk). That data lives in ClickHouse, which the enterprise cannot reach: it imports
only `server/app` and never `server/internal/*` (ADR-0001), and `RouteContext`
exposed only the control-plane Postgres pool, not the data-plane read layer. Rather
than let the enterprise open its own ClickHouse connection and re-encode the
core-owned `summaries` schema (which would drift the moment the core repartitions or
renames a column), the core exposes the reads it already knows how to do. This ADR
records that seam.

## Decision

- **Two funcs on `RouteContext`, not a ClickHouse pool for the enterprise.**
  `Usage(ctx, tenantID) (UsageStats, error)` returns one tenant's volumetry;
  `LastIngestByTenant(ctx) (map[string]time.Time, error)` returns the most recent
  ingest per tenant in one scan (so the account list flags live vs churned accounts
  without an N+1). Both are always non-nil, because ClickHouse is the data plane and
  is always present (unlike the optional control-plane pool). The seam does **no**
  authorization; the composing build gates it behind its own operator allowlist.

- **The series count is the guardrail's key, computed in the core.** `UsageStats.Series`
  counts distinct `(method, route_template, status_class)` — exactly
  `guardrail.SeriesKey` (service and instance excluded). Because the core owns both the
  cap-metering definition and this count, "series vs cap" stays honest; if the
  enterprise re-encoded the key it would silently diverge when the core changed it.

- **Windows follow the rollup TTLs.** Liveness (first/last ingest) reads the 1-day tier
  (730-day TTL, so a churned tenant still resolves a real last-ingest); cardinality and
  the 30-day request total read the 1-minute tier (30-day TTL, preserves every series
  dimension); the disk estimate reuses `PerformanceStats` over the raw tier
  (`rows × measured avg on-disk row size` from `system.parts`, with a constant
  fallback). A never-ingested tenant yields the zero value, not an error.

- **Cross-tenant reads get a first-class `Operator()` handle.** `QueryService` still
  exposes no tenant-taking method — the tenant-scoped dashboard path is reached only
  through `Tenant(id)`, keeping a scoped read from accidentally spanning tenants.
  `LastIngestByTenant` is a deliberate cross-tenant read, so it lives behind a
  dedicated `QueryService.Operator()` handle: cross-tenant access is explicit and
  greppable at every call site instead of smuggled into the scoped path.

## Consequences

- The enterprise depends on a stable Go API (`app.UsageStats`), never on ClickHouse SQL
  or DSNs. The core keeps sole ownership of its schema and can evolve it freely.
- The seam is read-only and enterprise-motivated but not enterprise-only: a self-hoster
  can build the same operator view on the community binary.
- `mountExtensions` grew two parameters. If its positional signature keeps growing it
  should move to a context struct; deferred, not done here.
- "Disk" remains an estimate by construction: `summaries` is partitioned by day, not by
  tenant, so per-tenant on-disk bytes are not directly measurable. It must never be
  presented as a billing figure.
