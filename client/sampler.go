package maping

import (
	"runtime"
	"time"

	mapingv1 "github.com/arhuman/maping/proto/maping/v1"
)

// sampler captures per-window process resource gauges (USE: CPU, memory,
// goroutines, GC pause) that ride along with summary uploads, so a latency rise
// can be correlated with saturation without a release. It holds the previous
// cumulative counters (GC-pause, CPU, GC count, total-alloc, mallocs) so each
// window reports a DELTA rather than an ever-growing total. It is touched only from the uploader goroutine (via
// flush/buildRequest and Shutdown), so it needs no locking.
type sampler struct {
	prevPauseTotalNs uint64
	prevCPUNs        uint64
	prevNumGC        uint64
	prevTotalAlloc   uint64
	prevMallocs      uint64
	primed           bool // false until the first sample sets the baselines
}

// sample reads a fresh runtime snapshot and builds an InstanceWindow for the
// window [start, end]. cpu_ns and gc_pause_ns are reported as the delta since the
// previous sample; the first call primes the baselines and reports zero for them
// (there is no prior window to diff against). Memory and goroutine counts are
// point-in-time reads. ReadMemStats stops the world briefly but only once per
// flush window (seconds apart), so its cost is negligible against the flush.
func (s *sampler) sample(start, end time.Time) *mapingv1.InstanceWindow {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	cpu := cpuTimeNs()

	var gcDelta, cpuDelta, numGCDelta, totalAllocDelta, mallocsDelta uint64
	if s.primed {
		gcDelta = ms.PauseTotalNs - s.prevPauseTotalNs
		if cpu >= s.prevCPUNs {
			cpuDelta = cpu - s.prevCPUNs
		}
		numGCDelta = uint64(ms.NumGC) - s.prevNumGC
		totalAllocDelta = ms.TotalAlloc - s.prevTotalAlloc
		mallocsDelta = ms.Mallocs - s.prevMallocs
	}
	s.prevPauseTotalNs = ms.PauseTotalNs
	s.prevCPUNs = cpu
	s.prevNumGC = uint64(ms.NumGC)
	s.prevTotalAlloc = ms.TotalAlloc
	s.prevMallocs = ms.Mallocs
	s.primed = true

	return &mapingv1.InstanceWindow{
		WindowStartMs:   start.UnixMilli(),
		WindowEndMs:     end.UnixMilli(),
		CpuNs:           cpuDelta,
		RssBytes:        ms.Sys,
		HeapAllocBytes:  ms.HeapAlloc,
		GcPauseNs:       gcDelta,
		Goroutines:      uint64(runtime.NumGoroutine()),
		NumGc:           numGCDelta,
		TotalAllocBytes: totalAllocDelta,
		Mallocs:         mallocsDelta,
		GcCpuFraction:   ms.GCCPUFraction,
		HeapInuseBytes:  ms.HeapInuse,
		Gomaxprocs:      uint32(runtime.GOMAXPROCS(0)),
	}
}
