package maping

import (
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestSamplerFirstSampleHasZeroDeltas(t *testing.T) {
	var s sampler
	iw := s.sample(time.UnixMilli(1000), time.UnixMilli(11000), 7)

	assert.Equal(t, int64(1000), iw.GetWindowStartMs())
	assert.Equal(t, int64(11000), iw.GetWindowEndMs())
	// The first sample only establishes the baselines for the cumulative counters,
	// so their per-window deltas are zero (no prior window to diff against).
	assert.Zero(t, iw.GetCpuNs(), "first sample reports no CPU delta")
	assert.Zero(t, iw.GetGcPauseNs(), "first sample reports no GC-pause delta")
	assert.Zero(t, iw.GetNumGc(), "first sample reports no GC-count delta")
	assert.Zero(t, iw.GetTotalAllocBytes(), "first sample reports no total-alloc delta")
	assert.Zero(t, iw.GetMallocs(), "first sample reports no mallocs delta")
	// Point-in-time gauges are read live, so they are populated immediately.
	assert.Positive(t, iw.GetGoroutines(), "at least this test goroutine is running")
	assert.Positive(t, iw.GetHeapAllocBytes(), "a running program has live heap")
	assert.Positive(t, iw.GetHeapInuseBytes(), "a running program has in-use heap")
	assert.Positive(t, iw.GetGomaxprocs(), "GOMAXPROCS is at least 1")
	// post_gc_heap_bytes is a live gauge from runtime/metrics; a just-started process
	// may not have completed a GC yet, so its value is nondeterministic. Exercise the
	// read path without asserting a specific value.
	_ = iw.GetPostGcHeapBytes()
	// rss_true_bytes is best-effort per OS: true RSS on Linux, 0 elsewhere.
	if runtime.GOOS == "linux" {
		assert.Positive(t, iw.GetRssTrueBytes(), "Linux reads true RSS from /proc/self/statm")
	} else {
		assert.Zero(t, iw.GetRssTrueBytes(), "true RSS is 0 on non-Linux hosts")
	}
	// open_fds and fd_limit are best-effort per OS: Linux reads /proc/self/fd and
	// RLIMIT_NOFILE, other hosts report 0.
	if runtime.GOOS == "linux" {
		assert.Positive(t, iw.GetOpenFds(), "Linux counts entries in /proc/self/fd")
		assert.Positive(t, iw.GetFdLimit(), "Linux reads the soft RLIMIT_NOFILE ceiling")
	} else {
		assert.Zero(t, iw.GetOpenFds(), "open_fds is 0 on non-Linux hosts")
		assert.Zero(t, iw.GetFdLimit(), "fd_limit is 0 on non-Linux hosts")
	}
	// in_flight is the peak concurrency passed in by the caller (recorder-owned).
	assert.Equal(t, uint64(7), iw.GetInFlight(), "in_flight is the peak passed to sample")
	assert.True(t, s.primed, "the first sample primes the sampler")
}

func TestSamplerSecondSampleDeltasAreMonotonic(t *testing.T) {
	var s sampler
	s.sample(time.UnixMilli(0), time.UnixMilli(1), 0)
	prevPause := s.prevPauseTotalNs

	iw := s.sample(time.UnixMilli(1), time.UnixMilli(2), 0)
	// Deltas come from monotonic runtime counters, so the second window's GC-pause
	// delta equals the advance of the cumulative total since the first sample.
	assert.Equal(t, s.prevPauseTotalNs-prevPause, iw.GetGcPauseNs())
}
