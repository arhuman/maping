# Security Policy

## Supported versions

mAPI-ng is pre-1.0: every module in the workspace (`proto`, `client`, the client
adapters, and `server`) is released at the same version, and only the latest
minor receives security fixes. There are no backports to earlier minors.

| Version | Supported |
|---------|-----------|
| 0.12.x  | Yes |
| < 0.12  | No, upgrade to the latest minor |

## Reporting a vulnerability

**Do not open a public issue for a security problem.**

Report privately through GitHub: go to the
[Security tab](https://github.com/arhuman/maping/security/advisories/new) of
this repository and choose "Report a vulnerability". This opens a private
advisory visible only to the maintainers.

Please include:

- what an attacker can do, and what access they need to start;
- the affected module and version (`server`, `client`, an adapter, `proto`);
- reproduction steps, ideally a minimal program or request;
- any log output or stack trace, with secrets redacted.

## What to expect

- Acknowledgement within 7 days.
- An assessment (accepted, needs more information, or not a vulnerability) within 14 days.
- Fixes ship in the next minor release. If a report is accepted and severe, the
  release is cut sooner rather than waiting for other work.
- Credit in the release notes and the advisory, unless you ask otherwise.

## Scope

In scope: the collector server, the client libraries and adapters, the wire
protocol, and the published container image.

Out of scope: findings that require an already-compromised host or a malicious
operator with access to `.env`; vulnerabilities in third-party dependencies with
no exploitable path through this code (report those upstream); and the
deliberately vulnerable fault endpoints in `example/`, which exist to generate
diagnostic signals and are never meant for deployment.

## Operator notes

Two configuration facts matter for a secure deployment:

- `make up` runs `make preflight` first, which refuses to start production while
  `.env` still holds `env.sample` defaults (empty session key, template
  passwords, non-https base URL). Do not bypass it.
- `MAPING_TRUST_KEY_ORIGIN=1` makes the client accept the collector origin
  embedded in an ingest key even outside the hosted domain. Set it only for a
  self-hosted deployment whose keys you issue yourself, since it lets whoever
  issued the key choose where telemetry and the bearer secret are sent.
