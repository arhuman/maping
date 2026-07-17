package web

import (
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/arhuman/maping/server/internal/storage"
)

// verdictView is the server-computed endpoint-detail health verdict shown as a
// banner above the KPI strip. Level is one of Healthy/Degraded/Critical/Unknown;
// Headline equals Level; DotClass reuses the shared dot palette; Qualifier is an
// optional blast-radius note (e.g. "low traffic") that tempers a non-Healthy read
// without changing the metric-driven severity; Sentence is a single factual line;
// Open drives auto-expanding the Diagnostic details disclosure when not Healthy.
type verdictView struct {
	Level     string
	DotClass  string
	Headline  string
	Qualifier string
	Sentence  string
	Open      bool
}

// severity is the per-component verdict contribution: none, degraded, or
// critical. The endpoint verdict is the max severity across the error, spread,
// and latency-vs-baseline components.
type severity int

const (
	sevNone severity = iota
	sevDegraded
	sevCritical
)

// Latency floors (percentiles are in seconds throughout the detail view): 100ms,
// 200ms and 800ms as fractions of a second.
const (
	floor100ms = 0.1
	floor200ms = 0.2
	floor800ms = 0.8
)

// minBaselineBuckets is how many trailing 1m buckets (with traffic) the baseline
// needs before its median p95 is trusted as a comparison point. Below this the
// latency-vs-baseline rule is skipped rather than fabricated.
const minBaselineBuckets = 30

// minVerdictSamples is the request count a window needs before its percentile-based
// rules (latency-vs-baseline, spread) are trusted. Below it those rules are skipped,
// and — absent a confident error signal — the verdict is Unknown rather than a shaky
// Healthy. Errors are judged separately and are NOT subject to this gate.
const minVerdictSamples = 20

// lowTrafficRate (requests/second) is the blast-radius floor below which a non-Healthy
// verdict is tagged "low traffic": genuinely broken, but not a live high-volume
// incident. Severity itself stays metric-driven; this only tempers the read. A v1
// default meant to be tuned.
const lowTrafficRate = 0.05

// computeVerdict grades an endpoint window from its RED headline, a lagged trailing
// baseline series, and the window length (for the traffic-rate qualifier).
// Percentiles are in seconds. Confidence is handled per metric: errors are a
// confident fact judged at any volume, while the percentile rules (latency, spread)
// are skipped below minVerdictSamples. A window too small to judge latency and with
// no error signal is Unknown, never a shaky Healthy. Severity stays metric-driven; a
// broken but near-idle endpoint carries a "low traffic" qualifier rather than a
// discounted level. MAD-based robust scale is deferred to the diagnosis-engine slice.
func computeVerdict(d detailView, baseline []storage.TimePoint, winSeconds float64) verdictView {
	errors := int(math.Round(d.ErrorRate * float64(d.Count)))

	// Errors are judged on their own error-count floor: a handful of real failures
	// is a confident fact regardless of total volume, so error severity is NOT gated
	// by the sample-size check that guards the noisy percentile rules below.
	errSev := errorSeverity(errors, d.ErrorRate)

	// Latency and spread are percentile-based and unreliable on small samples, so
	// they are graded only once the window carries enough requests.
	enoughSamples := d.Count >= minVerdictSamples
	spread, latRatio := 0.0, 0.0
	spreadSev, latSev := sevNone, sevNone
	if enoughSamples {
		if d.P50 > 0 {
			spread = d.P95 / d.P50
		}
		spreadSev = spreadSeverity(spread, d.P95, d.Count)
		if baselineP95, ok := baselineMedianP95(baseline); ok && baselineP95 > 0 {
			latRatio = d.P95 / baselineP95
			latSev = latencySeverity(d.P95, latRatio)
		}
	}

	level := errSev
	if spreadSev > level {
		level = spreadSev
	}
	if latSev > level {
		level = latSev
	}

	// Blast-radius qualifier: a broken but near-idle endpoint is not a live incident.
	qualifier := ""
	if winSeconds > 0 && float64(d.Count)/winSeconds < lowTrafficRate {
		qualifier = "low traffic"
	}

	if level == sevNone {
		if !enoughSamples {
			return verdictView{
				Level:    "Unknown",
				DotClass: "dot-muted",
				Headline: "Unknown",
				Sentence: "Insufficient traffic (n=" + strconv.FormatUint(d.Count, 10) + ") — no verdict this window.",
			}
		}
		return verdictView{
			Level:    "Healthy",
			DotClass: "dot-ok",
			Headline: "Healthy",
			Sentence: healthySentence(d, spread),
		}
	}

	name, dot := "Degraded", "dot-warn"
	if level == sevCritical {
		name, dot = "Critical", "dot-err"
	}
	return verdictView{
		Level:     name,
		DotClass:  dot,
		Headline:  name,
		Qualifier: qualifier,
		Sentence:  problemSentence(d, errors, spread, latRatio, errSev, spreadSev, latSev),
		Open:      true,
	}
}

// errorSeverity grades the error-rate component. The absolute error-count floor
// (>=5 / >=10) is AND-ed with the rate so a couple of errors never trips a verdict;
// the low-traffic gate in computeVerdict (count < 20 -> Unknown) already screens out
// windows too small to judge. There is deliberately no total-request-count floor
// here: a 100% error rate must read Critical, not Healthy, on modest traffic.
func errorSeverity(errors int, rate float64) severity {
	switch {
	case errors >= 10 && rate >= 0.05:
		return sevCritical
	case errors >= 5 && rate >= 0.01:
		return sevDegraded
	default:
		return sevNone
	}
}

// spreadSeverity grades the p95/p50 tail-spread component, floored on p95 and
// count so a wide ratio on trivially fast or low-traffic windows is ignored.
func spreadSeverity(spread, p95 float64, count uint64) severity {
	switch {
	case spread >= 6 && p95 >= floor200ms && count >= 50:
		return sevCritical
	case spread >= 2.5 && p95 >= floor100ms && count >= 50:
		return sevDegraded
	default:
		return sevNone
	}
}

// latencySeverity grades p95 against the trailing baseline, AND-ing an absolute
// floor with the relative multiple so a small absolute p95 never trips purely on
// ratio.
func latencySeverity(p95, ratio float64) severity {
	switch {
	case p95 >= floor800ms && ratio >= 4:
		return sevCritical
	case p95 >= floor200ms && ratio >= 2:
		return sevDegraded
	default:
		return sevNone
	}
}

// baselineMedianP95 collects the p95 of every baseline bucket that saw traffic
// and returns their median. It reports false when fewer than minBaselineBuckets
// carried traffic, in which case the latency-vs-baseline rule is skipped.
func baselineMedianP95(baseline []storage.TimePoint) (float64, bool) {
	p95s := make([]float64, 0, len(baseline))
	for _, b := range baseline {
		if b.Count > 0 {
			p95s = append(p95s, b.P95)
		}
	}
	if len(p95s) < minBaselineBuckets {
		return 0, false
	}
	sort.Float64s(p95s)
	return median(p95s), true
}

// median returns the median of a pre-sorted, non-empty slice.
func median(sorted []float64) float64 {
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// healthySentence is the one-line Healthy summary: error rate, p95, and a plain
// word for the tail spread.
func healthySentence(d detailView, spread float64) string {
	word := "wide tail"
	switch {
	case spread < 1.3:
		word = "stable"
	case spread < 2.5:
		word = "steady"
	}
	return fmtPctD(d.ErrorRate) + " errors, p95 " + fmtMsFull(d.P95) + ", " + word + " latency."
}

// problemSentence composes the Degraded/Critical line from the components that
// actually fired, strongest severity first (latency, error, spread breaking
// ties), each phrased factually with the shared latency/percentage formatters.
func problemSentence(d detailView, errors int, spread, latRatio float64, errSev, spreadSev, latSev severity) string {
	type reason struct {
		sev  severity
		rank int
		text string
	}
	var reasons []reason
	if latSev != sevNone {
		reasons = append(reasons, reason{latSev, 0, "p95 " + fmtMsFull(d.P95) + " (" + fmtRatio(latRatio) + " baseline)"})
	}
	if errSev != sevNone {
		reasons = append(reasons, reason{errSev, 1, fmtPctD(d.ErrorRate) + " errors"})
	}
	if spreadSev != sevNone {
		reasons = append(reasons, reason{spreadSev, 2, "spread " + fmtRatio(spread) + " (p95 " + fmtMsFull(d.P95) + ")"})
	}
	sort.SliceStable(reasons, func(i, j int) bool {
		if reasons[i].sev != reasons[j].sev {
			return reasons[i].sev > reasons[j].sev
		}
		return reasons[i].rank < reasons[j].rank
	})
	parts := make([]string, 0, len(reasons))
	for _, r := range reasons {
		parts = append(parts, r.text)
	}
	return strings.Join(parts, "; ") + "."
}

// fmtRatio renders a multiple with one decimal, e.g. 8.7× — the verdict-sentence
// form of the p95-over-baseline and tail-spread ratios.
func fmtRatio(x float64) string {
	return strconv.FormatFloat(x, 'f', 1, 64) + "×"
}
