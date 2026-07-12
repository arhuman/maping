---
status: accepted
---

# Connect on the client, gRPC on the server

The mAPI-ng client library talks to the collector using **Connect (connectrpc.com)**
configured for the **gRPC wire protocol**, over HTTP/2, with **protobuf** payloads
and **zstd** compression. The server terminates native **gRPC**. Connect's client
is wire-compatible with a gRPC server, so this is one protocol, two libraries.

## Why not grpc-go on the client too

The client is injected into arbitrary host API binaries. `google.golang.org/grpc`
pulls a large, opinionated dependency tree and is a common source of version
conflicts in host apps — directly at odds with the "always safe to add" zero-config
promise. Connect is built on the standard `net/http` stack, has a far smaller
footprint, and degrades to HTTP/1.1 on hostile networks. The workload (one small
batched, fire-and-forget upload every ~10s) needs none of gRPC's streaming/
multiplexing strengths, so the heavier client buys nothing.

## Consequences

Reversible if needed — Connect and gRPC are wire-compatible, so the client
transport can be swapped without touching the server. protobuf schema evolution
rules apply to the wire format.
