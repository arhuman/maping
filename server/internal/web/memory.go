package web

import (
	"strconv"

	"github.com/arhuman/maping/server/internal/storage"
)

// memoryVerdict is the server-computed leak-vs-burst read for the instances
// serving an endpoint, shown as a standalone card in the diagnostic disclosure.
// It mirrors verdictView's shape (Level/DotClass/Sentence) and adds the evidence
// a transparent, threshold-based rule owes an operator: the concrete numbers it
// fired on, a discrete confidence tier, and a falsifier. Memory is a per-process
// property of the instances (instance_windows has no endpoint dimension), so this
// judges the fleet behind the endpoint, not the endpoint itself. Show is false
// when there is no verdict (Unknown / no data) so the card suppresses entirely.
type memoryVerdict struct {
	Level      string
	DotClass   string
	Sentence   string
	Confidence string
	Evidence   []string
	Falsifier  string
	Show       bool
}

// Tuneable defaults for the leak-vs-burst rule. These are v1 legibility knobs,
// meant to be tuned against real /fault data, not theoretical constants.
const (
	// minMemorySamples is how many non-zero post-GC buckets the window needs
	// before a verdict is trusted. Below it the read is Unknown (never a false
	// "stable"), because a two-point series cannot tell a trend from noise.
	minMemorySamples = 4

	// leakRiseRatio is the last-over-first post-GC-heap multiple that, combined
	// with the absolute floor and the sustained check, reads as a leak.
	leakRiseRatio = 1.5
	// leakRiseFloorBytes is the absolute rise (last - first) a leak must clear so
	// heap noise on a small baseline cannot trip the ratio. 32 MiB.
	leakRiseFloorBytes = 32 << 20

	// burstPeakRatio is the interior-peak-over-baseline multiple (post-GC or
	// in-use heap) that marks a spike.
	burstPeakRatio = 1.5
	// burstReturnRatio is how close to the first bucket the last bucket must land
	// for a spike to count as returned (a burst, not a leak).
	burstReturnRatio = 1.2

	// leakStrongRatio and confidentSamples lift the discrete confidence tier from
	// Medium to High: a clear doubling / a long-enough series.
	leakStrongRatio  = 2.0
	confidentSamples = 8
)

// computeMemoryVerdict grades a per-window fleet memory trend as Leak, Burst,
// Stable, or Unknown. It reads post-GC live heap as the primary leak signal (a
// baseline that steps up and holds), with in-use heap as a secondary burst
// signal. The rule is deliberately transparent and threshold-based: every branch
// is reconstructable from the Evidence bullets it emits. Below minMemorySamples
// non-zero buckets it returns Unknown rather than fabricate a trend.
func computeMemoryVerdict(points []storage.MemoryTrendPoint) memoryVerdict {
	postGC := make([]uint64, 0, len(points))
	inuse := make([]uint64, 0, len(points))
	for _, p := range points {
		if p.PostGCHeapBytes == 0 {
			continue // unpopulated (older client) or empty bucket: not a sample
		}
		postGC = append(postGC, p.PostGCHeapBytes)
		inuse = append(inuse, p.HeapInuseBytes)
	}

	n := len(postGC)
	if n < minMemorySamples {
		return memoryVerdict{
			Level:    "Unknown",
			DotClass: "dot-muted",
			Sentence: "Insufficient memory samples (n=" + strconv.Itoa(n) + ") — no leak/burst verdict this window.",
		}
	}

	first, last := postGC[0], postGC[n-1]
	peakPostGC := maxU64(postGC)
	peakInuse := maxU64(inuse)
	firstInuse := inuse[0]

	// Sustained: the second half's low sits at or above the first half's high, so
	// the baseline stepped up and held rather than bumping transiently.
	firstHalfHigh := maxU64(postGC[:n/2])
	secondHalfLow := minU64(postGC[n/2:])
	sustained := secondHalfLow >= firstHalfHigh

	rise := int64(last) - int64(first)
	leakRatioMet := float64(last) >= leakRiseRatio*float64(first)
	leakFloorMet := rise >= leakRiseFloorBytes

	switch {
	case leakRatioMet && leakFloorMet && sustained:
		return leakVerdict(first, last, secondHalfLow, firstHalfHigh, n)
	case isBurst(first, last, firstInuse, peakPostGC, peakInuse):
		return burstVerdict(first, last, peakPostGC, peakInuse, firstInuse, n)
	default:
		return stableVerdict(minU64(postGC), peakPostGC, n)
	}
}

// isBurst reports whether an interior post-GC or in-use heap peak spiked past the
// burst ratio but the last post-GC bucket returned near the first — a spike that
// came back, distinct from a leak that held.
func isBurst(first, last, firstInuse, peakPostGC, peakInuse uint64) bool {
	spiked := float64(peakPostGC) >= burstPeakRatio*float64(first) ||
		(firstInuse > 0 && float64(peakInuse) >= burstPeakRatio*float64(firstInuse))
	returned := float64(last) <= burstReturnRatio*float64(first)
	return spiked && returned
}

func leakVerdict(first, last, secondLow, firstHigh uint64, n int) memoryVerdict {
	ratio := float64(last) / float64(first)
	conf := "Medium"
	if ratio >= leakStrongRatio && n >= confidentSamples {
		conf = "High"
	}
	return memoryVerdict{
		Level:      "Leak",
		DotClass:   "dot-err",
		Confidence: conf,
		Sentence:   "Post-GC heap rose " + memBytes(first) + " → " + memBytes(last) + " and held — likely a leak.",
		Evidence: []string{
			"Post-GC live heap: " + memBytes(first) + " at window start → " + memBytes(last) + " at end (" + fmtRatio(ratio) + " over " + strconv.Itoa(n) + " buckets).",
			"Rise of " + memBytes(last-first) + " clears the " + memBytes(leakRiseFloorBytes) + " noise floor.",
			"Sustained: the second half's low (" + memBytes(secondLow) + ") sits at or above the first half's high (" + memBytes(firstHigh) + ").",
		},
		Falsifier: "Not a leak if the heap returns to ~" + memBytes(first) + " after the next GC — re-check over a longer window.",
		Show:      true,
	}
}

func burstVerdict(first, last, peakPostGC, peakInuse, firstInuse uint64, n int) memoryVerdict {
	peak, base := peakPostGC, first
	if firstInuse > 0 && peakInuse > peakPostGC {
		peak, base = peakInuse, firstInuse
	}
	ratio := 0.0
	if base > 0 {
		ratio = float64(peak) / float64(base)
	}
	conf := "Medium"
	if n >= confidentSamples {
		conf = "High"
	}
	return memoryVerdict{
		Level:      "Burst",
		DotClass:   "dot-warn",
		Confidence: conf,
		Sentence:   "Post-GC heap peaked at " + memBytes(peakPostGC) + " then returned to " + memBytes(last) + " — an allocation burst, not a leak.",
		Evidence: []string{
			"Peak heap " + memBytes(peak) + " reached " + fmtRatio(ratio) + " the " + memBytes(base) + " baseline mid-window.",
			"Last bucket back to " + memBytes(last) + ", within " + fmtRatio(burstReturnRatio) + " of the " + memBytes(first) + " start.",
		},
		Falsifier: "A true leak if the baseline keeps climbing next window instead of holding near " + memBytes(first) + ".",
		Show:      true,
	}
}

func stableVerdict(low, high uint64, n int) memoryVerdict {
	ratio := 1.0
	if low > 0 {
		ratio = float64(high) / float64(low)
	}
	conf := "Medium"
	if n >= confidentSamples {
		conf = "High"
	}
	return memoryVerdict{
		Level:      "Stable",
		DotClass:   "dot-ok",
		Confidence: conf,
		Sentence:   "Post-GC heap held near " + memBytes(low) + " across the window — no leak signal.",
		Evidence: []string{
			"Post-GC live heap ranged " + memBytes(low) + "–" + memBytes(high) + " over " + strconv.Itoa(n) + " buckets (" + fmtRatio(ratio) + " spread).",
			"No sustained rise above the " + memBytes(leakRiseFloorBytes) + " noise floor.",
		},
		Show: true,
	}
}

// memBytes formats a uint64 byte count with the shared fmtBytes helper (1024-based
// units, e.g. "240 MB").
func memBytes(v uint64) string { return fmtBytes(float64(v)) }

// maxU64 returns the largest element of a non-empty slice.
func maxU64(xs []uint64) uint64 {
	m := xs[0]
	for _, x := range xs[1:] {
		if x > m {
			m = x
		}
	}
	return m
}

// minU64 returns the smallest element of a non-empty slice.
func minU64(xs []uint64) uint64 {
	m := xs[0]
	for _, x := range xs[1:] {
		if x < m {
			m = x
		}
	}
	return m
}
