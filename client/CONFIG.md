# mAPI-ng Client Configuration Spec

The client's guiding contract is **zero-config**: one input (`MAPING_KEY`),
everything else inferred, and an **absent key makes the middleware a no-op** so
adding mAPI-ng to a codebase is always safe (docs/context.md → *Zero-config*). This
document pins the precedence and validation rules so behavior is unambiguous
before any code depends on it.

## Precedence

For every setting: **explicit code option > environment variable > default.**

```go
r.Use(maping.Middleware(
    maping.WithKey("..."),          // beats MAPING_KEY
    maping.WithEndpoint("..."),     // beats MAPING_ENDPOINT
    maping.WithService("checkout"), // beats derived/env service name
))
```

## Settings

| Setting | Code option | Env var | Default | Required |
|---|---|---|---|---|
| Ingest key | `WithKey` | `MAPING_KEY` | *(none)* | **Yes to activate.** Absent ⇒ no-op recorder. |
| Endpoint | `WithEndpoint` | `MAPING_ENDPOINT` | trusted key-embedded origin, else hosted collector URL (baked in) | No |
| Service name | `WithService` | `MAPING_SERVICE` | derived: `OTEL_SERVICE_NAME` → binary name (`os.Args[0]`) | No |
| Instance id | `WithInstance` | `MAPING_INSTANCE` | derived: `HOSTNAME`/pod name → OS hostname | No |
| Flush window | `WithFlushWindow` | `MAPING_FLUSH_SECONDS` | 10s | No |

The key **encodes the tenant** (Q5); the client never configures a tenant id.

### Endpoint precedence (full)

`WithEndpoint` > `MAPING_ENDPOINT` > key-embedded origin (if trusted) > default.

The ingest key may embed the collector origin (`mk_live_<origin>.<secret>`),
letting a single `MAPING_KEY` configure both credential and endpoint: the
normal zero-config path for the hosted service, since a hosted key always
embeds a hosted origin. That embedded origin is used only when no explicit
endpoint (option or env) is set, only if it is a valid `http(s)` URL, and
only if it is under the trusted hosted-service domain; otherwise the client
logs one `Warn` and falls back to the default endpoint. Set
`MAPING_TRUST_KEY_ORIGIN=1` to trust an embedded origin outside that domain
anyway (e.g. a self-hosted deployment whose own keys embed a private
domain). An operator-supplied endpoint (option or env var) is never subject
to this check: it is always used as given.

## TLS / endpoint rules

- Default endpoint is `https://`; the transport requires TLS in production.
- An `http://` endpoint is permitted for **local/dev only** and triggers **H2C**
  (cleartext HTTP/2) so gRPC-over-HTTP/2 works without TLS (review fix #7).
- Any other scheme is invalid config (see below).

## Invalid-config behavior

The client **never** panics or blocks the host on bad config. On any invalid
value (unparseable endpoint, bad flush window, malformed key format):

1. Log **once** at `Warn` via the host's logger (rate-limited), naming the
   offending setting.
2. Fall back to the **no-op recorder** — the host is never affected.

This mirrors the fail-open data contract (Q7): setup problems are loud in logs
and the dashboard, but never fatal to the host API.

## Tests (implemented in M1 with the `Config` type)

Table-driven, covering: precedence (code beats env beats default), key-absent ⇒
no-op, derivation order for service/instance, endpoint scheme validation
(https/http-H2C/invalid), and flush-window parsing/clamping.
