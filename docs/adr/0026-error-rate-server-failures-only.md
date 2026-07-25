---
status: accepted
---

# RED error rate counts server failures only (5xx + NO_STATUS)

The RED error rate is currently `count(4XX + 5XX + NO_STATUS) / total`, defined
identically at six places: the five `error_count` `sumIf` expressions in
`storage/dashboard.go`, `storage/debug.go`, and `storage/query.go`, plus the Go-side
`c4xx + c5xx + cno` blend in `dashboard.go`. That one rate feeds `errorSeverity` in
`web/verdict.go` (`errors >= 10 && rate >= 5%` → Critical; `errors >= 5 && rate >= 1%`
→ Degraded), and the verdict gates the diagnosis engine (ADR-0021), which only runs on
a Degraded or Critical endpoint.

The result is a false-positive on the flagship feature. Client errors (4xx: the caller
sent something unauthorized, malformed, or for a missing resource) are counted as
service failures. An auth-gated endpoint sitting at a steady 6% 401 rate from expired
tokens and scanners reads Critical and triggers a ranked-cause diagnosis, even though
the service is healthy. Blending client errors into the failure signal inflates the
headline number and desensitizes operators to it. `NO_STATUS` is different: it means the
handler never produced a status (timeout, cancel, or crash, per the proto
`NoStatusReason` enum), which is a genuine server-side failure.

## Decision

- **The failure error rate is `5XX + NO_STATUS` only.** 4xx (client errors) leave the
  failure rate. `NO_STATUS` stays, it is a server-side failure. Apply this at all six
  definition sites (the five `error_count` `sumIf` queries and the one Go blend), so
  there is a single meaning of "error rate" across services, endpoints, versions, and
  instances.
- **The verdict thresholds are unchanged.** `errorSeverity` keeps its numbers but now
  grades the server-failure rate, so Degraded/Critical (and therefore the diagnosis
  engine) fire on server failures and latency, never on client-error noise. The
  diagnosis "why" line (`Error rate X%`) now reflects server failures only.
- **4xx is surfaced, not discarded.** The RED display gains a distinct client-error
  (4xx) rate alongside the failure rate, so an auth-dependency outage, a bad client
  deploy, or a broken route (mass 401 / 400 / 404) stays visible. Its signal is in the
  delta, not the baseline, so it belongs on its own line, not blended into "is the
  service failing".
- **No storage change.** `status_class` already separates 2xx/3xx/4xx/5xx/NO_STATUS
  (ADR-0012), so this is a query and display redefinition, retroactive to all existing
  data. No migration, no re-ingest, no backfill.

## Consequences

- Every historical error-rate number drops by its client-error share, and endpoints that
  were Critical purely from 4xx read Healthy (correctly). This changes the meaning of a
  headline metric on every dashboard, so it must be communicated at release: numbers read
  lower after deploy by design, and any external alerting keyed on the old blended rate
  shifts to the new 4xx line for client-error spikes.
- The diagnosis engine stops firing on healthy, client-error-heavy endpoints. That is the
  primary payoff.
- A minority of endpoints legitimately treat some 4xx as failures (a payments API caring
  about 402, an API whose 400 spike means a contract break). They are momentarily
  under-counted until the configurable layer below exists. This is the accepted trade:
  the default is right for the majority and low-noise; the exceptions get a scalpel later.

## Deferred (explicitly out of scope here)

The opposite risk, that one global rule cannot capture per-endpoint error semantics, is
real but is not solved by weakening this default. Two later phases are recorded so this
ADR is not mistaken for a rejection of configurability:

- **Phase 2:** a single deployment-wide env override to treat extra codes as errors,
  keeping the zero-config-file property.
- **Phase 3:** per-service and per-endpoint classification with a chip UI, a
  control-plane store, and the dashboard's first user-mutation surface (likely a
  Team/Enterprise feature). Per-code granularity is cheap because the exact
  `status_codes Map(UInt32, UInt64)` is already stored, so it needs no migration; the
  cost is moving the error-rate query off the coarse `status_class` `sumIf` on the hot
  path, and the diagnosis trigger must honor the per-endpoint classification. Its own ADR
  when built.
