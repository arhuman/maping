---
status: accepted
---

# Tenant-scoped data-plane access: make an un-scoped query unrepresentable

Data-plane multitenancy is row-level and software-enforced: every ClickHouse row
carries a server-resolved `tenant`, and there is no infrastructure wall between
tenants (see [`context.md`](../context.md) → *Data plane*). The correctness of isolation therefore
rests on every query and every write carrying the right tenant. We make that
invariant a property of the **types**, not of caller discipline.

## Decision

**Reads go through a tenant-bound handle.** `storage.QueryService` exposes exactly
one method, `Tenant(tenant string) TenantQuery`. The five read operations
(`SeriesOverTime`, `Services`, `Endpoints`, `EndpointDetail`, `HasAnySummary`)
live on `TenantQuery` and no longer take a `tenant` parameter — the tenant is a
bound field. The only way to reach a query is `qs.Tenant(t).Method(...)`, so an
un-scoped read is unrepresentable rather than merely discouraged.

**The raw connection never leaves the storage package.** `Writer` no longer
exposes `Conn() driver.Conn`. Instead it offers `Migrate(ctx, log)` (schema DDL)
and `QueryService()` (a scoped read handle). Combined with `Enqueue(Row)` for
writes — where `Row` already carries the server-stamped tenant — the three ways to
touch the data plane are all tenant-aware or schema-only. There is no general
escape hatch for arbitrary un-scoped SQL.

**The web layer depends on interfaces, not the concrete handle.** `web.Querier`
is `Tenant(tenant) ScopedQuery`, with `ScopedQuery` holding the tenant-free method
set. `storage.TenantQuery` structurally satisfies `web.ScopedQuery`; because
`*QueryService.Tenant` returns the concrete `storage.TenantQuery` (a concrete
return type does not satisfy an interface-returning method), a one-method
`scopedQuerier` adapter bridges `*QueryService` to `web.Querier` at the
composition root (`server/internal/app/deps.go`). Storage must not import web, so
the bridge lives in `app`, not `storage`.

## Why not a per-call tenant parameter

The previous shape passed `tenant string` as the first argument to every query.
That is a valid design, but the tenant is then just one string among several
(service, method, route): a caller can pass the wrong one, an empty one, or forget
to resolve it, and nothing catches it at compile time. For a system whose entire
tenant isolation is software-enforced, binding the tenant into a handle removes a
whole class of cross-tenant-leak bugs at the type level — the same reasoning as
"make illegal states unrepresentable".

## Consequences

- The read path cannot be called without a tenant; the raw `driver.Conn` is
  encapsulated. Integration tests open their own connection and are unaffected.
- A small adapter (`scopedQuerier`) exists in `app` purely to reconcile the
  concrete-vs-interface return-type rule. It is the price of keeping storage free
  of a web import.
- The **write path is not yet symmetric**: `Enqueue` trusts that ingest stamped
  `Row.Tenant`; the type does not force a non-empty tenant. A follow-up (a
  tenant-scoped writer, or a fail-closed empty-tenant check in `Enqueue`) would
  extend the same guarantee to writes. Tracked as a known gap.
