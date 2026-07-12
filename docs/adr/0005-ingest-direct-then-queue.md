---
status: accepted
---

# Direct batched ClickHouse writes for v1, durable queue later

For v1 the collector writes Summaries **directly to ClickHouse**, using in-collector
batching plus ClickHouse `async_insert` (`async_insert=1` + `wait_for_async_insert=1`,
no Buffer-table engine — review fix #1) to avoid the many-small-inserts
problem. A durable queue (Redpanda) between collector and ClickHouse is **planned but
deferred**.

## Why

The textbook high-throughput design puts a durable log (Redpanda/Kafka/NATS
JetStream) between a thin stateless accept tier and a batching ClickHouse writer,
decoupling accept-scaling from store-scaling and surviving ClickHouse downtime with no
data loss. But for an early product that is materially more to operate, and the queue
can be added later **without changing the wire contract or the client** — so the cost
of deferring is low and reversible. We take the simpler v1 ops and accept the risk
that a ClickHouse outage pushes backpressure to clients (which drop-oldest, ADR/Q7)
and can lose an outage window of data.

## Migration trigger

Introduce the Redpanda queue when any of: sustained ingest approaches ClickHouse
insert limits despite batching; ClickHouse "too many parts"/merge pressure appears;
or data-loss during ClickHouse maintenance windows becomes unacceptable. At that
point the collector becomes append-to-log only and a separate consumer owns all
ClickHouse writes.
