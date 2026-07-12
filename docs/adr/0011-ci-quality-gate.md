---
status: accepted
---

# CI quality gate: run the Makefile targets on every push and PR

The repository carries a strong quality apparatus — `golangci-lint` with the
project's complexity/length thresholds, `govulncheck`, `go test -race`, a
file-length guard (`make checklen`), and `//go:build integration` suites for the
ClickHouse and Postgres critical paths. Before this decision none of it ran
automatically: every guarantee depended on a developer running `make` locally, and
the integration tests ran essentially never. Green meant "green on someone's
laptop", not "green on `main`".

## Decision

Add a GitHub Actions workflow (`.github/workflows/ci.yml`) that **runs the same
Makefile targets developers run locally**, so CI and local checks can never
diverge. Three jobs, on `push` to `main` and on every `pull_request`:

- **test** — `make build` + `make test` (`-race -short -cover`), matrixed over the
  `go.mod` floor and the latest stable Go.
- **lint** — `make audit` (`go mod verify` + `golangci-lint` at the pinned
  v2.12.2 + `govulncheck`, per module; `make checklen` runs first inside `audit`).
- **integration** — the `//go:build integration` storage and control-plane tests
  against live `clickhouse:24.8` and `postgres:17` service containers, so both
  DB-bound critical paths are exercised automatically rather than on demand.

Driving the Makefile (rather than re-encoding `go test`/`golangci-lint` invocations
in YAML) is deliberate: it keeps a single source of truth for what "the gate" is,
so a change to `make audit`/`make test` is reflected in CI for free.

## Why not a coverage gate (yet)

A hard coverage floor was considered and deferred. Several packages are
legitimately low in the default (`-short`) run — `storage`/`control` DB methods are
covered by the integration job, and `cmd`/`app` are wiring — so a naive repo-wide
floor would fail the build for non-defects. The intended path is to add a
**ratcheting** floor once the integration coverage is folded into the reported
number, rather than a blunt threshold now.

## Consequences

- `main` is now gated: a lint regression, a race, a vulnerability in called code,
  a file over the length limit, or an integration-path break fails CI.
- CI cost is bounded (short unit run + one integration job with two service
  containers). The integration job is the slowest; it is justified because it is
  the only automatic coverage of the ClickHouse/Postgres critical paths.
- No coverage floor yet, so a coverage regression still passes — the ratcheting
  gate is the follow-up. The `release` path (goreleaser/tags) is out of scope for
  this ADR.
