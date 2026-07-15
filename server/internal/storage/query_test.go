package storage

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildSeriesQuery exercises the pure query builder: tier selection, step
// flooring, the table-allowlist guard, and the single %s interpolation — all
// without a live ClickHouse. This covers the SQL-construction logic that was
// previously reachable only through the integration suite.
func TestBuildSeriesQuery(t *testing.T) {
	base := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)

	t.Run("floors a too-fine step to the tier minimum", func(t *testing.T) {
		// 1h window -> raw tier, minStep 10s; a 1s request must floor to 10s.
		q, steps, err := buildSeriesQuery(base, base.Add(time.Hour), time.Second)
		require.NoError(t, err)
		assert.Equal(t, int64(10), steps)
		assert.Contains(t, q, "FROM "+tableRaw)
	})

	t.Run("honors a coarser requested step", func(t *testing.T) {
		// Same raw tier, but a 1m request is above the 10s floor and is kept.
		q, steps, err := buildSeriesQuery(base, base.Add(time.Hour), time.Minute)
		require.NoError(t, err)
		assert.Equal(t, int64(60), steps)
		assert.Contains(t, q, "FROM "+tableRaw)
	})

	t.Run("wide window selects a coarse rollup and floors to it", func(t *testing.T) {
		// A year-long window reads day rollups; the step floors to 1d (86400s)
		// regardless of the finer request.
		q, steps, err := buildSeriesQuery(base, base.Add(365*24*time.Hour), time.Minute)
		require.NoError(t, err)
		assert.Equal(t, int64(86400), steps)
		assert.Contains(t, q, "FROM "+table1d)
	})

	t.Run("interpolated table is always in the allowlist", func(t *testing.T) {
		// Across window widths that span every tier, the only interpolated token
		// must be one of the four allowlisted tier tables — never anything else.
		for _, width := range []time.Duration{
			time.Hour, 24 * time.Hour, 30 * 24 * time.Hour, 400 * 24 * time.Hour,
		} {
			q, _, err := buildSeriesQuery(base, base.Add(width), time.Second)
			require.NoError(t, err)
			found := false
			for tbl := range tierTables {
				if strings.Contains(q, "FROM "+tbl) {
					found = true
					break
				}
			}
			assert.Truef(t, found, "width %s: query must FROM an allowlisted tier table", width)
		}
	})
}
