---
status: accepted
---

# Leak-vs-burst memory verdict on the endpoint detail page

ADR-0018 added `post_gc_heap_bytes` (the live heap as of the last GC mark) to the
instance-window stream precisely so a rising baseline could tell a memory leak from an
allocation burst. That instrumentation shipped, but nothing read it beyond a raw number in
the Resources table. An operator staring at a per-instance "POST-GC HEAP" cell still has to
eyeball a trend and reason about leak-vs-burst themselves.

## Decision

Add a server-side analysis and a standalone card in the endpoint-detail diagnostic
disclosure that reads the per-window memory telemetry over the chart's own window and
states, in one factual line with its evidence, whether the process is leaking or just had a
burst. No proto or schema change: the fields already exist.

- **Window = the chart range.** The memory trend and the rule use the same `from`/`to`/`step`
  the detail-page timeline already uses (`SeriesOverTime`'s window), so heap and latency read
  over one window. A new `MemoryTrendForService` query buckets `instance_windows` by that
  step and takes the fleet peak (`max`) of `post_gc_heap_bytes` and `heap_inuse_bytes` per
  bucket. It is service-scoped (memory is a per-process property of the instances serving the
  endpoint; `instance_windows` has no endpoint dimension), tenant-scoped, and half-open over
  the window.

- **Transparent, threshold-based rule** (`computeMemoryVerdict`, tuneable constants at the
  top of `memory.go`), graded from the post-GC series with in-use heap as a secondary burst
  signal:
  - Fewer than `minMemorySamples` (4) non-zero post-GC buckets -> **Unknown** (card
    suppressed), never a fabricated "stable".
  - **Leak** when the last bucket is >= `leakRiseRatio` (1.5x) the first AND the rise clears
    `leakRiseFloorBytes` (32 MiB, so noise on a small baseline cannot trip it) AND the rise is
    sustained (the second half's low sits at or above the first half's high — the baseline
    stepped up and held).
  - **Burst** (only if not a leak) when an interior post-GC or in-use peak reaches
    `burstPeakRatio` (1.5x) the baseline but the last bucket returned within `burstReturnRatio`
    (1.2x) the first — spiked and came back.
  - Otherwise **Stable**.

- **Confidence is a discrete tier ("High"/"Medium"), not a percentage**, per the §9.3
  decision: a rule with a handful of buckets cannot honestly claim a precise probability, and
  a tier communicates the epistemic weight without false precision. High requires a clear
  signal (a doubling for a leak) and a long-enough series; otherwise Medium.

- The card renders the verdict sentence + dot + confidence, an inline SVG memory-trend chart
  (post-GC heap as the primary line, in-use heap as a lighter secondary line, byte y-axis),
  the concrete evidence bullets, and a falsifier line. It shows for Leak/Burst/Stable and
  suppresses entirely on Unknown, matching the page's quiet-when-healthy philosophy.

- A Leak or Burst signal OR-s the diagnostic disclosure open so the card surfaces even when
  the top health banner is flat (a leak can present with steady errors and unchanged latency).
  The banner's own level, headline, and sentence are untouched — the memory read is additive,
  not a re-grading of endpoint health.

## Scope

This is a self-contained card. The full ranked-cause diagnosis engine is out of scope and
will later absorb this rule as one of its inputs. The thresholds are v1 legibility defaults
meant to be tuned against the `/fault/*` example test-bed, not theoretical constants.

## Consequences

- The post-GC instrumentation of ADR-0018 now pays off: the leak-vs-burst distinction it was
  built for is stated in words, not left to the operator's eye.
- Memory is judged for the instances behind the endpoint, not the endpoint itself; the copy
  says so, so the service-scoped read is not mistaken for a per-endpoint one.
- The rule is reconstructable from its own evidence bullets, so a surprising verdict can be
  audited against the numbers rather than trusted blindly.
