---
status: accepted
---

# Composition seams: out-of-tree features via app.Run options

The server is open-core (ADR-0004): this repository carries the complete
community collector, while any out-of-tree feature set — commercial or
otherwise — must compose *into the same binary shape* without its source ever
entering this tree. Config-gating cannot deliver that (gated code is still
published code), and Go's `internal/` rule forbids an external module from even
naming the core's types. The question this ADR answers: how does a separate
module extend the server when it cannot import anything under
`server/internal/*` and the core must not know it exists?

## Decision

**`server/app` is the composition root and the only public extension surface.**
A composing build lives in its own module, imports `server/app` alone, and
passes functional options to `app.Run`:

- **`WithRoutes(RouteRegistrar)`** — mount extra HTTP surfaces after the core
  routes. The registrar receives a `RouteContext` carrying the mux, the
  control-plane pool, the logger, and two *capability functions*: `Gate` (the
  dashboard auth middleware) and `SessionOrg` (the verified caller org), so an
  extension can serve authenticated routes without touching `internal/auth`.
- **`WithBackgroundJob(BackgroundJob)`** — long-running tasks on the server's
  lifecycle (started after boot, cancelled at shutdown, sharing the pool).
- **`WithExtraMigrations(fs.FS, dir)`** — additional control-plane migration
  sources applied after the embedded core migrations, so out-of-tree schema
  layers on top without forking the core migration history.
- **`WithLimitProvider(LimitProviderFactory)`** — decorate the core
  `LimitProvider` that drives the ingest guardrails (rate,
  cardinality, payload) with the composing build's own per-tenant policy.
- **`WithLoginInterceptor(LoginInterceptorFactory)`** — a post-authentication
  hook the OIDC callback consults before the default first-login flow. The
  hook receives a **`PostAuthContext`** capability (`SetSession`) so it can
  finish a login it resolved out of band while the session signer stays
  private to `auth`.
- **`WithMemberAdmin(MemberAdminFactory)`** — supply the team-panel backend;
  the panel's generic template ships in core and renders only when this is
  wired.
- **`WithPublicHome(http.HandlerFunc)`** — the anonymous-"/" landing page;
  companion routes register via `WithRoutes`.

Three mechanics make the surface workable from outside the module:

1. **Alias re-exports.** The types an extension must *implement*
   (`LimitProvider`, `Limits`, `LoginInterceptor`, `PostAuthContext`,
   `MemberAdmin`, `MemberInfo`, `InviteInfo`) are re-exported from `app` as
   type **aliases** of the internal types (`app/limits.go`, `app/compose.go`).
   An alias is the same type, so a value written against `app.X` *is* the
   internal `X` — no adapters, and `internal/` encapsulation is not weakened.
2. **Capability surfaces, not exported guts.** Where an extension needs a
   privileged action (start a session, gate a route, read the session org),
   it receives a narrow function or one-method interface minted by the core
   (`PostAuthContext`, `RouteContext.Gate`, `RouteContext.SessionOrg`). Keys,
   cookies, and context keys stay private.
3. **Pool factories.** Stateful options take `func(*pgxpool.Pool) T` so every
   extension store shares the single control-plane pool `Run` owns — a
   composed binary never opens a second pool.

## Why options, not plugins or gating

- **Config-gating publishes the code** — the opposite of the open-core
  boundary this exists to hold.
- **Go plugins** (`-buildmode=plugin`) are platform-fragile and version-locked;
  compile-time composition in the composing build's `main` is type-safe and
  boring.
- **Functional options degrade to zero**: the community binary is exactly
  `app.Run(log)` — every seam is nil-safe (no team panel, plain login,
  anonymous "/" redirects to /login, core migrations only), so composing
  nothing *is* the community behavior, not a stripped variant of it.

## The invariant

This tree contains no out-of-tree feature's source, in any commit; a composing
build imports only `server/app`. The compiler enforces the import direction;
source-absence is held by review (a CI forbidden-symbol guard is the intended
hardening).

## Consequences

- New extension points are deliberate API work: adding one means an option, a
  nil-safe default, and (if types cross the boundary) an alias — reviewed like
  any public surface.
- A feature that needs several options (migrations + interceptor + admin +
  routes) must be wired as a unit; composing builds should bundle each
  feature's options behind one constructor so a half-wired feature cannot
  compile-and-misbehave.
- The seam list is the honest catalog of what the core considers replaceable;
  anything not on it is core behavior by decision, not omission.
