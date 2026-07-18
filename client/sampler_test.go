package maping

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestSamplerFirstSampleHasZeroDeltas(t *testing.T) {
	var s sampler
	iw := s.sample(time.UnixMilli(1000), time.UnixMilli(11000))

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
	assert.True(t, s.primed, "the first sample primes the sampler")
}

func TestSamplerSecondSampleDeltasAreMonotonic(t *testing.T) {
	var s sampler
	s.sample(time.UnixMilli(0), time.UnixMilli(1))
	prevPause := s.prevPauseTotalNs

	iw := s.sample(time.UnixMilli(1), time.UnixMilli(2))
	// Deltas come from monotonic runtime counters, so the second window's GC-pause
	// delta equals the advance of the cumulative total since the first sample.
	assert.Equal(t, s.prevPauseTotalNs-prevPause, iw.GetGcPauseNs())
}
