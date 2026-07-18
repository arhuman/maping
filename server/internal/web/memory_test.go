package web

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arhuman/maping/server/internal/storage"
)

const mib = 1 << 20

// memSeries builds a MemoryTrendPoint series from post-GC heap values (in bytes),
// setting in-use heap equal to post-GC so tests that don't exercise the in-use
// burst path stay simple. Timestamps are one minute apart.
func memSeries(postGC ...uint64) []storage.MemoryTrendPoint {
	base := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	pts := make([]storage.MemoryTrendPoint, len(postGC))
	for i, v := range postGC {
		pts[i] = storage.MemoryTrendPoint{
			TS:              base.Add(time.Duration(i) * time.Minute),
			PostGCHeapBytes: v,
			HeapInuseBytes:  v,
		}
	}
	return pts
}

func TestComputeMemoryVerdict(t *testing.T) {
	tests := []struct {
		name  string
		pts   []storage.MemoryTrendPoint
		level string
		show  bool
	}{
		{
			// Staircase: post-GC heap steps up and holds -> Leak.
			name:  "staircase leak",
			pts:   memSeries(200*mib, 220*mib, 400*mib, 460*mib, 700*mib, 760*mib),
			level: "Leak",
			show:  true,
		},
		{
			// Sawtooth: spikes mid-window then returns to the baseline -> Burst.
			name:  "sawtooth burst",
			pts:   memSeries(240*mib, 250*mib, 600*mib, 800*mib, 300*mib, 250*mib),
			level: "Burst",
			show:  true,
		},
		{
			// Flat within the noise floor -> Stable.
			name:  "flat stable",
			pts:   memSeries(240*mib, 245*mib, 238*mib, 242*mib, 241*mib, 239*mib),
			level: "Stable",
			show:  true,
		},
		{
			// Too few non-zero buckets -> Unknown, suppressed.
			name:  "two points unknown",
			pts:   memSeries(240*mib, 800*mib),
			level: "Unknown",
			show:  false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mv := computeMemoryVerdict(tc.pts)
			assert.Equal(t, tc.level, mv.Level)
			assert.Equal(t, tc.show, mv.Show)
			if tc.show {
				require.NotEmpty(t, mv.Sentence, "shown verdict carries a sentence")
				assert.NotEmpty(t, mv.Evidence, "shown verdict carries evidence bullets")
			}
		})
	}
}

// TestComputeMemoryVerdictSkipsZeroBuckets confirms unpopulated post-GC buckets
// (older clients / empty windows) do not count toward the sample floor: a series
// with only three non-zero buckets is Unknown even though it has six points.
func TestComputeMemoryVerdictSkipsZeroBuckets(t *testing.T) {
	mv := computeMemoryVerdict(memSeries(0, 240*mib, 0, 400*mib, 0, 760*mib))
	assert.Equal(t, "Unknown", mv.Level)
	assert.False(t, mv.Show)
}

// TestComputeMemoryVerdictRiseFloorGuardsNoise confirms a proportionally large
// rise on a tiny baseline (2 MiB -> 4 MiB, 2x but under the 32 MiB floor) is not
// a leak.
func TestComputeMemoryVerdictRiseFloorGuardsNoise(t *testing.T) {
	mv := computeMemoryVerdict(memSeries(2*mib, 2*mib, 3*mib, 3*mib, 4*mib, 4*mib))
	assert.NotEqual(t, "Leak", mv.Level)
}
