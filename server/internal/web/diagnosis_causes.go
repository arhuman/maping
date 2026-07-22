package web

import (
	"sort"
	"strconv"

	"github.com/arhuman/maping/server/internal/storage"
)

// This file holds the per-cause builders that computeDiagnosis (in diagnosis.go)
// ranks. Each returns a cause: whether it fired, how many signals corroborated
// it, its magnitude for tie-breaking, and the operator-facing evidence and
// falsifier. Every builder is a pure function of the already-loaded page signals.

// causeMemory absorbs the standalone leak-vs-burst card: it fires on a memory
// Leak (strong) or Burst, or on GC-CPU and alloc-rate both up vs baseline. Its
// evidence is the memory verdict's bullets plus any resource deltas, and it
// carries the memory-trend chart as its evidence visual.
func causeMemory(p diagnosisParams, win, base fleetResources) cause {
	c := cause{name: "Memory / GC pressure", dotClass: "dot-warn", maxSignals: 4}
	leak := p.Memory.Level == "Leak"
	burst := p.Memory.Level == "Burst"
	gcUp := win.gcCPU >= gcCPUElevated && isUp(win.gcCPU, base.gcCPU)
	allocUp := isUp(win.allocRate, base.allocRate)
	gcFreqUp := isUp(win.gcFreq, base.gcFreq)

	if !(leak || burst || (gcUp && allocUp)) {
		return c
	}
	c.fired = true
	if leak {
		c.dotClass = "dot-err"
	}

	var ev []string
	if leak || burst {
		c.signals++
		ev = append(ev, p.Memory.Evidence...)
	}
	if gcUp {
		c.signals++
		ev = append(ev, "GC CPU "+fmtPctD(win.gcCPU)+" vs "+fmtPctD(base.gcCPU)+" baseline.")
	}
	if allocUp {
		c.signals++
		ev = append(ev, "Alloc rate "+fmtBytes(win.allocRate)+"/s vs "+fmtBytes(base.allocRate)+"/s baseline.")
	}
	if gcFreqUp {
		c.signals++
		ev = append(ev, "GC frequency "+fmtRate(win.gcFreq)+"/s vs "+fmtRate(base.gcFreq)+"/s baseline.")
	}
	c.evidence = ev
	c.chart = p.MemoryChart
	c.falsifier = p.Memory.Falsifier
	if c.falsifier == "" {
		c.falsifier = "Not memory-driven if heap and GC CPU stay flat over a longer window while latency persists."
	}
	c.mag = win.gcCPU * 10
	if leak {
		c.mag += 3
	}
	return c
}

// causeCPU fires when the fleet's cores-used approaches its GOMAXPROCS budget —
// the endpoint is compute-bound (busy), the inverse of congestion (blocked).
func causeCPU(win fleetResources) cause {
	c := cause{name: "CPU saturation", dotClass: "dot-warn", maxSignals: 1}
	if win.gomaxprocs <= 0 {
		return c
	}
	ratio := win.cores / win.gomaxprocs
	if ratio < cpuSatRatio {
		return c
	}
	c.fired = true
	c.signals = 1
	c.mag = ratio
	c.evidence = []string{
		fmtCores(win.cores) + " used of " + strconv.FormatFloat(win.gomaxprocs, 'f', 0, 64) +
			" available (" + fmtPctD(ratio) + " of GOMAXPROCS).",
	}
	c.falsifier = "Not CPU-bound if cores-used drops well below GOMAXPROCS while latency stays high — look to congestion or downstream."
	return c
}

// causeCongestion is the key payoff: blocked, not busy. It fires when self-time
// dominates (downstream share low) AND CPU is flat AND GC is flat, yet in-flight
// concurrency is up or the fleet is near / climbing toward its fd ceiling. This is
// the cause that separates a connection/pool stall from raw compute pressure.
func causeCongestion(win, base fleetResources, ds downstreamView) cause {
	c := cause{name: "Connection / pool congestion", dotClass: "dot-warn", maxSignals: 4}

	selfDominant := !ds.HasData || ds.DownFraction <= (1-selfDominantShare)
	cpuFlat := !isUp(win.cores, base.cores)
	gcFlat := !isUp(win.gcCPU, base.gcCPU)
	if !(selfDominant && cpuFlat && gcFlat) {
		return c
	}
	inflightUp := isUp(float64(win.inFlight), float64(base.inFlight))
	fdNear := win.fdLimit > 0 && float64(win.openFDs)/float64(win.fdLimit) >= fdNearLimit
	fdUp := isUp(float64(win.openFDs), float64(base.openFDs))
	if !(inflightUp || fdNear || fdUp) {
		return c
	}

	c.fired = true
	c.signals = 2 // self-dominant + compute-flat: the "blocked, not busy" shape
	c.evidence = []string{
		"Self-time dominates (downstream " + fmtPctD(ds.DownFraction) + ") while CPU and GC held flat vs baseline — blocked, not busy.",
	}
	if inflightUp {
		c.signals++
		c.mag = float64(win.inFlight) / float64(base.inFlight)
		c.evidence = append(c.evidence, "In-flight concurrency peaked "+strconv.FormatUint(win.inFlight, 10)+" vs "+strconv.FormatUint(base.inFlight, 10)+" baseline.")
	}
	if fdNear || fdUp {
		c.signals++
		if win.fdLimit > 0 {
			c.evidence = append(c.evidence, "Open FDs peaked "+strconv.FormatUint(win.openFDs, 10)+" of "+strconv.FormatUint(win.fdLimit, 10)+" limit.")
		} else {
			c.evidence = append(c.evidence, "Open FDs peaked "+strconv.FormatUint(win.openFDs, 10)+" (up vs baseline).")
		}
		if r := float64(win.openFDs) / float64(win.fdLimit); win.fdLimit > 0 && r > c.mag {
			c.mag = r
		}
	}
	c.falsifier = "Not congestion if cores-used or GC CPU climbs with the latency — that points back to compute or memory."
	return c
}

// causeOverload fires when context-deadline/cancel aborts are an elevated share of
// traffic (from the NO_STATUS reasons), optionally corroborated by rising in-flight
// concurrency — requests timing out under load rather than a compute or memory fault.
func causeOverload(p diagnosisParams, win, base fleetResources) cause {
	c := cause{name: "Overload / timeouts", dotClass: "dot-warn", maxSignals: 2}
	if p.Detail.Count == 0 {
		return c
	}
	var aborts uint64
	for _, r := range p.NoStatus {
		if r.Reason == 1 || r.Reason == 2 { // canceled or deadline-exceeded
			aborts += r.Count
		}
	}
	share := float64(aborts) / float64(p.Detail.Count)
	if share < deadlineCancelShare {
		return c
	}
	c.fired = true
	c.signals = 1
	c.mag = share
	c.evidence = []string{
		"Deadline/cancel aborts " + strconv.FormatUint(aborts, 10) + " of " + strconv.FormatUint(p.Detail.Count, 10) + " requests (" + fmtPctD(share) + ").",
	}
	if isUp(float64(win.inFlight), float64(base.inFlight)) {
		c.signals++
		c.evidence = append(c.evidence, "In-flight concurrency peaked "+strconv.FormatUint(win.inFlight, 10)+" vs "+strconv.FormatUint(base.inFlight, 10)+" baseline.")
	}
	c.falsifier = "Not overload if the aborts persist at steady, low traffic — that is a downstream or timeout-config fault, not load."
	return c
}

// causeGoroutineLeak fires when the fleet's goroutine peak is both above an
// absolute floor and up vs baseline — an unbounded-growth signal distinct from a
// transient concurrency spike.
func causeGoroutineLeak(win, base fleetResources) cause {
	c := cause{name: "Goroutine leak", dotClass: "dot-warn", maxSignals: 2}
	if win.goroutines < goroutineHighFloor || !isUp(float64(win.goroutines), float64(base.goroutines)) {
		return c
	}
	c.fired = true
	c.signals = 2
	c.mag = float64(win.goroutines) / float64(base.goroutines)
	c.evidence = []string{
		"Goroutines peaked " + strconv.FormatUint(win.goroutines, 10) + " vs " + strconv.FormatUint(base.goroutines, 10) + " baseline, above the " + strconv.Itoa(goroutineHighFloor) + " floor.",
	}
	c.falsifier = "Not a leak if the goroutine count returns to baseline after load subsides — re-check over a longer window."
	return c
}

// causeDownstream fires when downstream calls dominate request time — the inverse
// of congestion: the endpoint is waiting on a dependency, not on its own resources.
func causeDownstream(ds downstreamView) cause {
	c := cause{name: "Downstream / IO", dotClass: "dot-warn", maxSignals: 1}
	if !ds.HasData || ds.DownFraction < downstreamHighShare {
		return c
	}
	c.fired = true
	c.signals = 1
	c.mag = ds.DownFraction * 6
	c.evidence = []string{
		"Downstream calls are " + fmtPctD(ds.DownFraction) + " of request time (self is only " + fmtPctD(1-ds.DownFraction) + ").",
	}
	c.falsifier = "Not downstream if self-time rises while downstream share falls — the endpoint's own work is the bottleneck."
	return c
}

// causeInstanceLocalized fires when at least one replica is a p95 outlier: the
// degradation is localized to that instance rather than fleet-wide, pointing at a
// bad host or a hot shard rather than the code path.
func causeInstanceLocalized(instances []instanceStatRow) cause {
	c := cause{name: "Instance-localized", dotClass: "dot-warn", maxSignals: 1}
	var p95s []float64
	for _, r := range instances {
		if r.Count > 0 {
			p95s = append(p95s, r.P95)
		}
	}
	if len(p95s) == 0 {
		return c
	}
	sorted := make([]float64, len(p95s))
	copy(sorted, p95s)
	sort.Float64s(sorted)
	fleetMed := median(sorted)

	var worst instanceStatRow
	found := false
	for _, r := range instances {
		if r.IsOutlier && (!found || r.P95 > worst.P95) {
			worst, found = r, true
		}
	}
	if !found {
		return c
	}
	c.fired = true
	c.signals = 1
	if fleetMed > 0 {
		c.mag = worst.P95 / fleetMed
	}
	c.evidence = []string{
		"Localized to " + worst.Instance + ": p95 " + fmtMsFull(worst.P95) + " vs " + fmtMsFull(fleetMed) + " fleet median.",
	}
	c.falsifier = "Not localized if the p95 gap closes when the instance drains — the outlier was a transient, not a bad host."
	return c
}

// causeRelease fires when one deploy_version carries a materially worse p95 than
// the best-performing version over the window — a release regression, attributable
// by rolling back rather than debugging the running code.
func causeRelease(versions []storage.VersionStat) cause {
	c := cause{name: "Release regression", dotClass: "dot-warn", maxSignals: 2}
	var best, worst storage.VersionStat
	haveBest, haveWorst := false, false
	for _, v := range versions {
		if v.Version == "" || v.Count < minVersionSamples || v.P95 <= 0 {
			continue
		}
		if !haveBest || v.P95 < best.P95 {
			best, haveBest = v, true
		}
		if !haveWorst || v.P95 > worst.P95 {
			worst, haveWorst = v, true
		}
	}
	if !haveBest || !haveWorst || best.Version == worst.Version {
		return c
	}
	ratio := worst.P95 / best.P95
	if ratio < releaseWorseRatio {
		return c
	}
	c.fired = true
	c.signals = 1
	c.mag = ratio
	c.evidence = []string{
		"Version " + worst.Version + " p95 " + fmtMsFull(worst.P95) + " vs " + best.Version + " p95 " + fmtMsFull(best.P95) + " (" + fmtRatio(ratio) + ").",
	}
	if worst.ErrorRate >= best.ErrorRate+0.01 {
		c.signals++
		c.evidence = append(c.evidence, "Version "+worst.Version+" error rate "+fmtPctD(worst.ErrorRate)+" vs "+best.Version+" "+fmtPctD(best.ErrorRate)+".")
	}
	c.falsifier = "Not a regression if both versions converge once traffic rebalances — the gap was a warm-up or a skewed sample."
	return c
}
