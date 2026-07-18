---
status: accepted
---

# Diagnosis engine on the endpoint detail page

The endpoint detail page collects a lot of telemetry — RED headline, per-instance USE
gauges, the memory trend, downstream split, versions, NO_STATUS reasons — but reading it was
left to the operator. ADR-0019 added a standalone leak-vs-burst memory card; ADR-0020 added
congestion gauges (`in_flight`, `open_fds`, `fd_limit`) precisely so a "blocked, not busy"
slowdown becomes visible. Nothing yet turns those signals into an investigation path: which
of memory, CPU, congestion, downstream, release, or an instance is the likely cause?

## Decision

Add a server-side **diagnosis engine** (`computeDiagnosis` in
`server/internal/web/diagnosis.go`) that correlates the signals already loaded on the detail
page into a ranked set of candidate causes and renders one ranked-cause card at the top of
the diagnostic disclosure. It absorbs the standalone memory card: the leak-vs-burst read
(`computeMemoryVerdict`, unchanged) becomes the Memory/GC cause's input, and the memory-trend
SVG becomes that cause's evidence chart.

- **Correlate, don't re-query.** The engine is a pure function over data the page already
  loads. The only addition is a `resourceBaseline` — a second `InstanceResourcesForService`
  call over the *same lagged trailing window* the verdict baseline already uses (6h ending 5m
  before `to`, so the incident cannot poison its own baseline). No proto, schema, or new query
  type: the resource baseline reuses the existing service-scoped query.

- **Gating (quiet when healthy).** Causes are computed only when the endpoint is anomalous:
  `Verdict.Level` is Degraded or Critical, OR the memory read is Leak or Burst. The memory OR
  preserves ADR-0019's flat-RED-leak surfacing: a leak that presents as steady errors with
  unchanged latency (a Healthy banner) still opens the card. A healthy endpoint with stable
  memory shows nothing.

- **Cause set (v1), each a small transparent rule** producing (fired, signals matched,
  evidence bullets, falsifier, internal score):
  1. **Memory / GC pressure** — memory Leak (strong) or Burst, and/or GC-CPU + alloc-rate +
     GC-frequency up vs baseline. Carries the memory-trend chart.
  2. **CPU saturation** — fleet cores-used / GOMAXPROCS `>= cpuSatRatio` (0.85): busy.
  3. **Connection / pool congestion** — the key payoff: self-time dominates (downstream
     share low) AND CPU flat AND GC flat, yet in-flight is up or the fleet is near/climbing its
     fd ceiling. Blocked, not busy — the cause that separates congestion from compute.
  4. **Overload / timeouts** — context-deadline/cancel aborts an elevated share of traffic
     (`>= deadlineCancelShare`, 5%), optionally corroborated by rising in-flight.
  5. **Goroutine leak** — fleet goroutine peak above an absolute floor AND up vs baseline.
  6. **Downstream / IO** — downstream share `>= downstreamHighShare` (0.5): the inverse of #3.
  7. **Instance-localized** — at least one replica is a p95 outlier (reuses `IsOutlier`).
  8. **Release regression** — one `deploy_version` carries a materially worse p95
     (`>= releaseWorseRatio`, 1.5×) than the best-performing version.

- **Discrete confidence, never a percentage** (per §9.3, as ADR-0019). Ranking is by an
  internal score that keeps *signal count dominant* (more corroboration ranks higher) and uses
  magnitude only to break ties. The score stays internal; the card surfaces a confidence tier
  (High ≥3 / Medium =2 / Low =1 signals matched) with the evidence count, e.g.
  "High (3/4 signals)".

- **Never false certainty.** Every top cause carries a falsifier ("what would change this
  read"). When the endpoint is anomalous but no rule fires, the card still shows with an
  **Unattributed** top cause ("no resource signal explains this — check the timeline and
  exemplars"), so the card never invents a cause.

- The card renders the top cause prominently (dot, name, confidence, a RED-derived `Why` line,
  a `Scope` blast-radius line), its evidence bullets, its chart when present, a falsifier, and
  a compact `Also considered` list of the other fired causes. It sits above the existing
  drill-down panels, which are unchanged.

## Scope

Server-side only (`server/internal/web` plus the reused baseline query). The thresholds are v1
legibility defaults meant to be tuned against the `/fault/*` example test-bed, not theoretical
constants. `computeVerdict` is untouched except for the existing `Open` auto-expand behavior.

## Consequences

- The USE and congestion instrumentation of ADR-0015/0017/0018/0020 now pays off: the signals
  are correlated into a named cause instead of a wall of gauges.
- Each cause is reconstructable from its own evidence bullets, so a surprising verdict can be
  audited against the numbers rather than trusted blindly.
- The standalone memory card is gone; its logic lives on as the Memory/GC cause, so there is
  one place a memory read surfaces, ranked against the alternatives.
- Adding a cause is a local change: a new rule function and a slot in the candidate list.
