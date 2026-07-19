# mAPI-ng Proto

`github.com/arhuman/maping/proto` — the **wire contract** shared by the mAPI-ng
client SDK and server. It is the one module both sides import, so client and
server agree on the grammar by construction.

**MIT licensed** (like `client`), so third parties can build against the protocol
directly. The schema is a **public, stable contract**: it evolves
backward-compatibly only, and `buf breaking` enforces that in CI from commit 1
(ADR-0002, ADR-0004).

## Packages

| Package | Import | What |
|---|---|---|
| `maping/v1` | `github.com/arhuman/maping/proto/maping/v1` | The protobuf schema (`maping.proto`) and generated types. Defines `IngestService` (`Register`, `Upload`) and its messages: `Envelope` (per-flush deploy identity), `Summary` (per-endpoint RED aggregate + DDSketch buckets, exemplars, error/no-status breakdowns), `InstanceWindow` (per-instance USE gauges), and the `Handshake`/`Upload` request-response pairs. The Connect service stubs live in `maping/v1/mapingv1connect`. |
| `mapingcompress` | `github.com/arhuman/maping/proto/mapingcompress` | The zstd Connect codec that is part of the wire contract. Both the client transport and the server ingest handler register it so zstd-compressed request bodies negotiate successfully (ADR-0002). |
| `token` | `github.com/arhuman/maping/proto/token` | The ingest-key wire format: `mk_live_<base64url(origin)>.<secret>`. The client derives its collector endpoint from the embedded origin; the server hashes and stores **only** the secret, so the origin is routing metadata that never affects auth. Handles the structured, keyless-origin, and bare-legacy forms. |

The DDSketch bucket layout (gamma/offset) is frozen and versioned by the schema —
see ADR-0001 for why the client and server must agree on it exactly.

## Development

Part of the repo's Go workspace (`go.work`), so build and test from anywhere in
the tree. The generated `*.pb.go` / `*.connect.go` are committed; regenerate them
from `maping.proto` with:

```bash
make proto        # buf generate (see buf.gen.yaml / buf.yaml at the repo root)
```

`buf breaking` runs in CI, so a schema change that would break existing clients
fails the build. Add fields, never renumber or remove them.

## License

MIT — see [`LICENSE`](LICENSE). The whole repository is MIT-licensed (ADR-0022).
