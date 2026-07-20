# Licensing

The whole mAPI-ng repository is released under the **MIT License**: every
module (`proto`, `client` and its framework adapters, `server`, `example`).
Each module directory carries its own MIT `LICENSE` file for tooling; the
root `LICENSE` is the same license and governs the repository as a whole.

The server was previously under the Business Source License 1.1 (BSL),
restricting non-competing use until a future conversion date. It has since
been relicensed to MIT, so that restriction no longer applies to any part of
the codebase.

## What this means

- **Self-hosting is free**, in every sense: you can run the collector and
  the dashboard yourself with no license fee and no field-of-use
  restriction. See [Self-hosting](/doc/self-hosting) for how.
- **Modifying and redistributing the code is permitted**, including
  building a competing hosted service from it. MIT grants that; there is no
  clause reserving hosted-service resale to the original maintainer.
- **The wire protocol is a stable, public contract.** The protobuf schema
  and the Connect/gRPC transport are documented and versioned so a
  third-party client or collector implementation can interoperate.

## What is commercial

The hosted service (a managed, zero-ops deployment with retention and SLA
guarantees) and account-management features built around it are separate
from this repository and are not covered by its MIT license. Running the
open-source server yourself, as described in [Self-hosting](/doc/self-hosting),
gives you the same collector, storage, and dashboard the hosted service
runs, without the operational commitment.
