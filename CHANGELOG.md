# Changelog

All notable user-facing changes to mAPI-ng are recorded here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and
the project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Every module in the workspace (`proto`, `client`, the client adapters, and
`server`) is released at the same version and tagged per module as
`<module>/vX.Y.Z`, so `client/v0.12.0` and `server/v0.12.0` are the same release.

## [Unreleased]

### Added
- `maping-server --version` prints the version and commit linked into the binary.
- `make cover` enforces a single coverage floor (`COVER_MIN`), and `make audit` depends on it.
- `make preflight` gates `make up`, refusing to start production when `.env` is missing or still carries `env.sample` defaults.

### Fixed
- The runtime container image runs as an unprivileged user instead of root.
- The client no longer falls back to an unreachable default ingest endpoint, and accepts a key-embedded origin only under the hosted domain unless `MAPING_TRUST_KEY_ORIGIN` is set.

## [0.12.0] - 2026-07-25

### Added
- Dashboard column for the 4xx client-error rate (ADR-0026).

### Changed
- The error rate excludes 4xx client errors, which are now reported separately (ADR-0026).

## [0.11.0] - 2026-07-22

### Added
- Operator usage seam exposing cross-tenant volumetry.
- Example app fault that correlates GC-pressure signals.

## [0.10.0] - 2026-07-22

### Added
- Auth maps one verified email to one organisation and exposes the member id on the seam.

## [0.9.0] - 2026-07-21

### Changed
- `/doc` renders inside the dashboard chrome for signed-in users.

## [0.8.0] - 2026-07-20

### Fixed
- `/doc` renders with the full site header injected.

## [0.7.0] - 2026-07-20

### Fixed
- `/doc` gained a top bar and a Documentation link in the sidebar.

## [0.6.0] - 2026-07-20

### Added
- Product documentation served at `/doc`.

### Changed
- The diagnosis "Falsifier" label reads "Rules this out".

## [0.5.0] - 2026-07-19

### Added
- Ranked-cause diagnosis engine on the dashboard.
- Runtime memory telemetry: MemStats per instance window, post-GC heap and true RSS gauges, file-descriptor count and in-flight congestion gauges.
- Leak versus burst memory verdict with a trend graph.
- Example app `/fault/*` test bed with leak, spike, churn and bloat faults, plus a one-shot traffic sweep.

## [0.4.0] - 2026-07-17

### Added
- Client middleware adapters for net/http, Echo v4, go-chi and Beego v2.
- Onboarding card switches its wire-up snippet per framework and shows the import block.

### Changed
- **Breaking**: extension pages render inside the dashboard shell.

## [0.3.0] - 2026-07-17

### Added
- `WithNavItem` injects links into the dashboard sidebar.
- `X-Content-Type-Options: nosniff` on every route, and HSTS on https deployments.

### Fixed
- The onboarding handshake polls in place instead of refreshing the whole page.

## [0.2.0] - 2026-07-16

### Added
- Collector, multi-tenant control plane, dashboard and composition seams.
- RED-plus telemetry: deploy and version labels, exemplars, and USE gauges.

### Fixed
- Exemplar arrays concatenate when same-key summary rows collapse.

## [0.1.0] - 2026-07-13

### Added
- First tagged release of the workspace modules.

[Unreleased]: https://github.com/arhuman/maping/compare/server/v0.12.0...HEAD
[0.12.0]: https://github.com/arhuman/maping/compare/server/v0.11.0...server/v0.12.0
[0.11.0]: https://github.com/arhuman/maping/compare/server/v0.10.0...server/v0.11.0
[0.10.0]: https://github.com/arhuman/maping/compare/server/v0.9.0...server/v0.10.0
[0.9.0]: https://github.com/arhuman/maping/compare/server/v0.8.0...server/v0.9.0
[0.8.0]: https://github.com/arhuman/maping/compare/server/v0.7.0...server/v0.8.0
[0.7.0]: https://github.com/arhuman/maping/compare/server/v0.6.0...server/v0.7.0
[0.6.0]: https://github.com/arhuman/maping/compare/server/v0.5.0...server/v0.6.0
[0.5.0]: https://github.com/arhuman/maping/compare/server/v0.4.0...server/v0.5.0
[0.4.0]: https://github.com/arhuman/maping/compare/server/v0.3.0...server/v0.4.0
[0.3.0]: https://github.com/arhuman/maping/compare/server/v0.2.0...server/v0.3.0
[0.2.0]: https://github.com/arhuman/maping/compare/server/v0.1.0...server/v0.2.0
[0.1.0]: https://github.com/arhuman/maping/releases/tag/server/v0.1.0
