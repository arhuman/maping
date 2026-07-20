# Self-hosting

The whole repository is MIT-licensed, so running mAPI-ng yourself, including
the collector and the dashboard, costs nothing beyond your own
infrastructure. See [Licensing](/doc/licensing) for what MIT covers.

## Run it with Docker Compose

```bash
cp env.sample .env    # then edit .env
make local            # laptop stack: publishes host ports
# or
make up               # production stack: publishes only the server port
```

The compose stack is a neutral base plus environment-specific overlays: the
local overlay publishes ClickHouse, Postgres, and the server on host ports
for development; the production overlay publishes only the server port, so
the data plane and control plane are reachable only on the internal Docker
network. `make up` runs a preflight check that rejects default or
obviously-weak production secrets before starting.

## Environment variables

Configuration is entirely environment variables (`MAPING_*`); there are no
config files. The template is `env.sample`. The variables that matter for a
self-hosted deployment:

| Variable | Purpose |
|---|---|
| `MAPING_PORT` | Dashboard and ingest port (default 8080) |
| `MAPING_CLICKHOUSE_DSN` | Data plane connection string (required) |
| `MAPING_POSTGRES_DSN` | Control plane connection string; empty disables auth (single dev tenant) |
| `MAPING_MAX_BODY_BYTES` | Pre-auth HTTP body size cap on ingest (default 4 MiB) |
| `MAPING_BASE_URL` | Public base URL, used to build OIDC callback URLs |
| `MAPING_SESSION_KEY` | Session/CSRF signing key, >= 32 bytes; required once `MAPING_POSTGRES_DSN` is set and the deployment is HTTPS |
| `MAPING_OIDC_GITHUB_CLIENT_ID` / `_SECRET` | GitHub login |
| `MAPING_OIDC_GOOGLE_CLIENT_ID` / `_SECRET` | Google login |

Without `MAPING_POSTGRES_DSN`, the server runs as a single-tenant dev
instance with no login: every Summary lands under one constant tenant, and
the dashboard is open at `/`. Setting it turns on multi-tenant auth; see
[Security & data flow](/doc/security-data-flow) for the three startup modes
this produces.

## Where data lives

- **Control plane (Postgres):** tenants, ingest keys, members, and plan
  limits. Small and transactional.
- **Data plane (ClickHouse):** Summaries and their rollups. This is where
  metrics data actually accumulates; retention is governed by the rollup
  tiers described in [Architecture](/doc/architecture).

Both are named services in the compose stack (`postgres`, `clickhouse`) with
their own persistent volumes; back them up like any other database you
operate.

## Instrumenting services against a self-hosted collector

Point clients at your own collector instead of the hosted default with
`MAPING_ENDPOINT`, or embed the origin in the ingest key itself if your key
issuance already encodes it. See [Quickstart](/doc/quickstart) for the
client setup.
