package web

import (
	"html/template"
	"sort"
	"strconv"
	"strings"

	"github.com/arhuman/maping/server/internal/storage"
)

// diagnosisView is the server-computed ranked-cause card shown at the top of the
// endpoint-detail diagnostic disclosure. It is a pure correlation of signals
// already loaded on the page — no new query beyond the resource baseline — that
// ranks candidate causes of an anomaly and surfaces the strongest with its
// evidence. Show is false when the endpoint is healthy (the card suppresses
// entirely); when true, TopCause always carries a falsifier so the read never
// claims false certainty. Others lists the remaining fired causes, compact.
type diagnosisView struct {
	Show     bool
	TopCause topCauseView
	Why      string // one line derived from RED ("Latency p95 6.4× the trailing baseline")
	Scope    string // blast radius ("2 of 8 instances · v1.8.3 · success and error")
	Others   []causeSummary
}

// topCauseView is the prominent ranked cause: its name, dot, discrete confidence
// (a tier plus the evidence count, never a percentage), the evidence bullets, a
// falsifier, and an optional chart (the memory trend, only when Memory is top).
type topCauseView struct {
	Name       string
	DotClass   string
	Confidence string // e.g. "High (3/3 signals)"
	Evidence   []string
	Falsifier  string
	Chart      template.HTML
}

// causeSummary is one of the other fired causes, shown compactly below the top
// cause so the operator sees the full ranked set without a wall of evidence.
type causeSummary struct {
	Name       string
	DotClass   string
	Confidence string
}

// diagnosisParams carries everything computeDiagnosis correlates. Every field is
// data already loaded on the detail page (plus the trailing ResourceBaseline,
// which reuses the existing InstanceResourcesForService query over the lagged
// window). A struct keeps the call site and the tests self-documenting.
type diagnosisParams struct {
	Detail           detailView
	Verdict          verdictView
	Resources        []storage.InstanceResourceStat // current window, per instance
	ResourceBaseline []storage.InstanceResourceStat // lagged trailing window, per instance
	Memory           memoryVerdict
	MemoryChart      template.HTML
	Instances        []instanceStatRow
	Versions         []storage.VersionStat
	Downstream       storage.DownstreamStat
	NoStatus         []storage.NoStatusReasonStat
	P95Baseline      float64
	P95BaselineOK    bool
	WinSeconds       float64
	BaselineSeconds  float64
}

// Tuneable diagnosis constants. These are v1 legibility knobs meant to be tuned
// against the /fault/* example test-bed, not theoretical thresholds.
const (
	// cpuSatRatio is the fleet cores-used / GOMAXPROCS fraction at or above which
	// the fleet reads as CPU-saturated (busy, not blocked).
	cpuSatRatio = 0.85

	// gcCPUElevated is the absolute GC-CPU fraction floor a window must clear
	// before a GC-CPU rise counts toward the Memory/GC cause.
	gcCPUElevated = 0.10

	// resourceRiseRatio is the window-over-baseline multiple (alloc rate, GC
	// frequency, in-flight, goroutines, open-fds) that reads as "up vs baseline".
	resourceRiseRatio = 1.5

	// downstreamHighShare is the downstream share of total request time at or
	// above which time is downstream-dominated (the Downstream/IO cause).
	downstreamHighShare = 0.5

	// selfDominantShare is the self-time share at or above which the endpoint is
	// spending its time on its own work — the precondition for congestion. It is
	// expressed as a downstream-share ceiling of 1-selfDominantShare.
	selfDominantShare = 0.6

	// fdNearLimit is the open-fds / fd-limit fraction at or above which the fleet
	// is near its file-descriptor ceiling (a congestion signal).
	fdNearLimit = 0.7

	// deadlineCancelShare is the context-deadline/cancel share of traffic at or
	// above which the overload/timeout cause fires.
	deadlineCancelShare = 0.05

	// goroutineHighFloor is the absolute goroutine count a fleet peak must clear
	// before a rise-vs-baseline reads as a goroutine leak.
	goroutineHighFloor = 500

	// releaseWorseRatio is the p95 multiple by which one deploy_version must
	// exceed the best-performing version to read as a release regression.
	releaseWorseRatio = 1.5
	// minVersionSamples is the request count a version needs before its RED is
	// trusted for regression attribution.
	minVersionSamples = 20
)

// fleetResources is the window-vs-baseline aggregate of the per-instance resource
// stats: delta counters become fleet-summed rates, gauges become the fleet peak.
// It is the comparison unit every resource-driven cause reads.
type fleetResources struct {
	cores      float64 // sum(cpu_ns) / window_ns — total cores consumed by the fleet
	gomaxprocs float64 // sum(GOMAXPROCS) across instances — the fleet's core budget
	gcCPU      float64 // max GC-CPU fraction across instances
	allocRate  float64 // sum(total_alloc) / winSeconds — fleet bytes/s
	gcFreq     float64 // sum(num_gc) / winSeconds — fleet GC cycles/s
	goroutines uint64  // max goroutine peak
	inFlight   uint64  // max in-flight peak
	openFDs    uint64  // max open-fd peak
	fdLimit    uint64  // max fd-limit ceiling
}

// aggregateFleet folds the per-instance resource stats into one fleet aggregate
// over a window: counters (cpu, alloc, num_gc) sum into rates, gauges take the
// peak. A non-positive window yields zero rates (the rate-based rules then skip).
func aggregateFleet(stats []storage.InstanceResourceStat, winSeconds float64) fleetResources {
	var f fleetResources
	if len(stats) == 0 {
		return f
	}
	var sumCPU, sumAlloc, sumNumGC, sumGomax uint64
	for _, s := range stats {
		sumCPU += s.CPUNs
		sumAlloc += s.TotalAllocBytes
		sumNumGC += s.NumGC
		sumGomax += uint64(s.GOMAXPROCS)
		if s.GCCPUFraction > f.gcCPU {
			f.gcCPU = s.GCCPUFraction
		}
		if s.Goroutines > f.goroutines {
			f.goroutines = s.Goroutines
		}
		if s.InFlight > f.inFlight {
			f.inFlight = s.InFlight
		}
		if s.OpenFDs > f.openFDs {
			f.openFDs = s.OpenFDs
		}
		if s.FDLimit > f.fdLimit {
			f.fdLimit = s.FDLimit
		}
	}
	f.gomaxprocs = float64(sumGomax)
	if winSeconds > 0 {
		f.cores = float64(sumCPU) / (winSeconds * 1e9)
		f.allocRate = float64(sumAlloc) / winSeconds
		f.gcFreq = float64(sumNumGC) / winSeconds
	}
	return f
}

// isUp reports whether cur has risen to at least resourceRiseRatio × base, with a
// positive base (a zero baseline cannot ground a ratio, so the signal is skipped).
func isUp(cur, base float64) bool {
	return base > 0 && cur >= resourceRiseRatio*base
}

// cause is one candidate cause under evaluation: whether it fired, how many
// signals corroborated it (with the max it could match, for the confidence
// tier), an internal ranking score, and the operator-facing evidence.
type cause struct {
	name       string
	dotClass   string
	fired      bool
	signals    int
	maxSignals int
	mag        float64 // magnitude, for ranking ties within a signal count
	evidence   []string
	falsifier  string
	chart      template.HTML
}

// score keeps signal count dominant (more corroboration ranks higher) and uses
// magnitude only to break ties within the same signal count. It is internal:
// the card surfaces the discrete confidence tier, never this number.
func (c cause) score() float64 {
	mag := c.mag
	if mag > 9 {
		mag = 9
	}
	return float64(c.signals)*10 + mag
}

// computeDiagnosis correlates the signals already on the detail page into a ranked
// set of candidate causes and returns the single top-cause card. It is a pure
// function: no I/O, deterministic in its inputs. The card shows only when the
// endpoint is anomalous (Degraded/Critical, or a memory Leak/Burst — the OR keeps
// a flat-RED leak surfacing); a healthy endpoint with stable memory shows nothing.
// When anomalous but no rule fires it still shows, with an Unattributed top cause,
// so the card never claims a certainty the data does not support.
func computeDiagnosis(p diagnosisParams) diagnosisView {
	anomalous := p.Verdict.Level == "Degraded" || p.Verdict.Level == "Critical" ||
		p.Memory.Level == "Leak" || p.Memory.Level == "Burst"
	if !anomalous {
		return diagnosisView{Show: false}
	}

	win := aggregateFleet(p.Resources, p.WinSeconds)
	base := aggregateFleet(p.ResourceBaseline, p.BaselineSeconds)
	ds := toDownstreamView(p.Downstream)

	candidates := []cause{
		causeMemory(p, win, base),
		causeCPU(win),
		causeCongestion(win, base, ds),
		causeOverload(p, win, base),
		causeGoroutineLeak(win, base),
		causeDownstream(ds),
		causeInstanceLocalized(p.Instances),
		causeRelease(p.Versions),
	}

	fired := make([]cause, 0, len(candidates))
	for _, c := range candidates {
		if c.fired {
			fired = append(fired, c)
		}
	}
	sort.SliceStable(fired, func(i, j int) bool {
		return fired[i].score() > fired[j].score()
	})

	why := diagnosisWhy(p.Detail, p.P95Baseline, p.P95BaselineOK)
	scope := diagnosisScope(p.Detail, p.Instances, p.Versions)

	if len(fired) == 0 {
		return diagnosisView{
			Show: true,
			TopCause: topCauseView{
				Name:       "Unattributed",
				DotClass:   "dot-muted",
				Confidence: "Low (0 signals)",
				Evidence:   []string{"No resource signal explains this degradation — check the timeline and exemplars."},
				Falsifier:  "A cause would attach if a resource, downstream, version, or instance signal crossed its threshold next window.",
			},
			Why:   why,
			Scope: scope,
		}
	}

	top := fired[0]
	view := diagnosisView{
		Show: true,
		TopCause: topCauseView{
			Name:       top.name,
			DotClass:   top.dotClass,
			Confidence: confidenceLabel(top.signals, top.maxSignals),
			Evidence:   top.evidence,
			Falsifier:  top.falsifier,
			Chart:      top.chart,
		},
		Why:   why,
		Scope: scope,
	}
	for _, c := range fired[1:] {
		view.Others = append(view.Others, causeSummary{
			Name:       c.name,
			DotClass:   c.dotClass,
			Confidence: confidenceLabel(c.signals, c.maxSignals),
		})
	}
	return view
}

// confidenceLabel renders the discrete confidence tier plus the evidence count:
// the tier communicates epistemic weight (a 3-signal cause is corroborated; a
// 1-signal cause fired on a single line of evidence), the count keeps it honest.
func confidenceLabel(signals, maxSignals int) string {
	tier := "Low"
	switch {
	case signals >= 3:
		tier = "High"
	case signals == 2:
		tier = "Medium"
	}
	return tier + " (" + strconv.Itoa(signals) + "/" + strconv.Itoa(maxSignals) + " signals)"
}

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

// diagnosisWhy renders the one-line RED framing of the anomaly: latency over
// baseline when the multiple is meaningful, else error rate, else tail spread.
func diagnosisWhy(d detailView, p95Base float64, ok bool) string {
	if ok && p95Base > 0 {
		if ratio := d.P95 / p95Base; ratio >= 2 {
			return "Latency p95 " + fmtRatio(ratio) + " the trailing baseline."
		}
	}
	if d.ErrorRate > 0 {
		return "Error rate " + fmtPctD(d.ErrorRate) + "."
	}
	if d.P50 > 0 {
		return "Tail spread p95/p50 " + fmtRatio(d.P95/d.P50) + "."
	}
	return "p95 " + fmtMsFull(d.P95) + "."
}

// diagnosisScope renders the blast radius: how many instances carry the anomaly
// (outliers of the trafficked fleet, or the whole fleet), the dominant version,
// and whether errors accompany the latency.
func diagnosisScope(d detailView, instances []instanceStatRow, versions []storage.VersionStat) string {
	total, outliers := 0, 0
	for _, r := range instances {
		if r.Count > 0 {
			total++
		}
		if r.IsOutlier {
			outliers++
		}
	}
	var parts []string
	switch {
	case total == 0:
		// no instance data — omit the instance clause
	case outliers > 0:
		parts = append(parts, strconv.Itoa(outliers)+" of "+strconv.Itoa(total)+" instances")
	default:
		parts = append(parts, strconv.Itoa(total)+" instances")
	}
	if v := dominantVersion(versions); v != "" {
		parts = append(parts, v)
	}
	if d.ErrorRate > 0 {
		parts = append(parts, "success and error")
	} else {
		parts = append(parts, "success only")
	}
	return strings.Join(parts, " · ")
}
