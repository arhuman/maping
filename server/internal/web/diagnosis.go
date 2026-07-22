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
