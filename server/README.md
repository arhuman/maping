# mAPI-ng Server

`maping-server` — the mAPI-ng collector, dashboard, and control plane.

**License: BSL 1.1** (Business Source License). Source-available for non-competing use.
Self-hosting is permitted; reselling as a competing hosted service is not. See `LICENSE`.

---

## Architecture

The server has two planes.

**Data plane** — ClickHouse stores Summaries in an `AggregatingMergeTree` table
(`summaries`). Latency is stored as a DDSketch (`Map(Int32, UInt64)` sparse bucket map).
Materialized views roll up raw 10s windows into 1-minute, 1-hour, and 1-day tiers with
retention TTLs. The query layer picks the appropriate tier automatically.

**Control plane** — Postgres stores tenants, ingest keys, members, and plan limits.
It is optional. When `MAPING_POSTGRES_DSN` is unset the server runs in static dev mode:
a fake `dev-key` resolver maps to a constant `dev-tenant`, default guardrails apply,
and no authentication is required.

**Ingest path** — the client sends Summaries over Connect/gRPC (protobuf). The server
receives them on a single HTTP listener (h2c + HTTP/1), validates the ingest key,
enforces guardrails (rate limit, cardinality cap), and writes to ClickHouse via an
in-process batcher with bounded-retry flush on shutdown.

**Dashboard** — a fixed 3-level RED view served as server-rendered HTML (Go
`html/template`, htmx, uPlot):

1. Service overview: rate / error% / p50 / p95 / p99 per service.
2. Endpoint table: server-side sortable by traffic, error rate, or p99.
3. Endpoint detail: DDSketch latency histogram + 4xx/5xx/no\_status breakdown + time-series chart.

A 4-step onboarding panel is shown until the first Summary arrives.

---

## Running locally

```bash
make local
```

That is the entire dev setup. `make local` starts the full dev stack (server + ClickHouse +
Postgres) with host ports published and the dashboard at http://localhost:8080. On first run
it auto-creates `.env` from `env.sample` — no manual `cp` step needed.

Auth is off by default: with `MAPING_POSTGRES_DSN` unset the server runs in static dev mode
(`dev-key` resolver, constant `dev-tenant`, no login required). Postgres stays unused until
you set that variable.

The ClickHouse schema is applied automatically at boot via `storage.ApplyMigrations` — no
manual migration step is required. Postgres control-plane migrations are likewise embedded
in the binary and applied automatically on startup when `MAPING_POSTGRES_DSN` is set.

### Running the server outside Docker (optional)

If you need to run the binary directly against a local ClickHouse instance:

```bash
make build
MAPING_CLICKHOUSE_DSN="clickhouse://maping:maping@localhost:9000/maping" \
    ./bin/maping-server
```

The server listens on `:8080` by default (override with `MAPING_LISTEN`).
With no `MAPING_POSTGRES_DSN` set, `dev-key` is the active ingest key and no login is required.

---

## Environment variables

### Required

| Variable | Default | Description |
|---|---|---|
| `MAPING_CLICKHOUSE_DSN` | `clickhouse://maping:maping@localhost:9000/maping` | ClickHouse connection string |

### Optional

| Variable | Default | Description |
|---|---|---|
| `MAPING_LISTEN` | `:8080` | Listen address |
| `MAPING_MAX_BODY_BYTES` | `4194304` (4 MiB) | Pre-auth HTTP body cap (hard memory-safety ceiling, enforced before the tenant is known). Invalid or non-positive values are ignored with a warning. |
| `MAPING_POSTGRES_DSN` | *(unset)* | Postgres DSN. Unset = static dev mode (no auth, `dev-key` resolver) |

### Auth variables (only when `MAPING_POSTGRES_DSN` is set)

| Variable | Default | Description |
|---|---|---|
| `MAPING_SESSION_KEY` | *(random ephemeral)* | HMAC signing key for session cookies, >= 32 bytes. Ephemeral key means sessions do not survive a restart. |
| `MAPING_BASE_URL` | *(unset)* | Public base URL (e.g. `https://mapi-ng.example.com`). Used to build OAuth redirect URIs and set `Secure` on cookies. |
| `MAPING_OIDC_GITHUB_CLIENT_ID` | *(unset)* | GitHub OAuth app client ID |
| `MAPING_OIDC_GITHUB_CLIENT_SECRET` | *(unset)* | GitHub OAuth app client secret |
| `MAPING_OIDC_GOOGLE_CLIENT_ID` | *(unset)* | Google OAuth client ID |
| `MAPING_OIDC_GOOGLE_CLIENT_SECRET` | *(unset)* | Google OAuth client secret |

---

## Auth modes

Three modes are selected at startup based on which variables are set:

| Mode | Condition | Effect |
|---|---|---|
| Auth off | No `MAPING_POSTGRES_DSN` | Constant `dev-tenant`; no login required. |
| Dev-login only | `MAPING_POSTGRES_DSN` set, no OIDC credentials | Login page with a single "dev admin" button; no real provider. |
| Real OIDC | `MAPING_POSTGRES_DSN` + GitHub or Google credentials set | GitHub/Google login; dev-login button is disabled. |

---

## Migrations

**ClickHouse** (data plane) — embedded in the binary and applied automatically at boot via `storage.ApplyMigrations`:

- `server/internal/storage/migrations/clickhouse/0001_summaries.sql` — `summaries` AggregatingMergeTree table.
- `server/internal/storage/migrations/clickhouse/0002_rollups.sql` — rollup materialized views (1m, 1h, 1d) and retention TTLs.

**Postgres** (control plane) — embedded in the binary and applied automatically on startup:

- `0001_control_plane.sql` — orgs, ingest keys, members, plan limits.
- `0002_handshakes.sql` — handshake records for the onboarding panel.
- `0003_member_oidc.sql` — unique partial index on `members(oidc_subject)` for OIDC identity.

---

## Make targets

| Target | Description |
|---|---|
| `make local` | Start the dev stack with host ports published; dashboard at http://localhost:8080 |
| `make up` | Start the production stack (only the server port published) |
| `make down` | Stop the stack |
| `make build` | Build `bin/maping-server` |
| `make test` | Run all tests with race detector across all modules |
| `make tidy` | Format code and tidy all modules |
| `make audit` | Vet, lint, and vulnerability scan across all modules |
| `make tools` | Install required Go dev tools (buf, protoc plugins, golangci-lint, govulncheck) |
| `make proto` | Lint and regenerate protobuf + Connect code |
