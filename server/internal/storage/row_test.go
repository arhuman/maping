package storage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arhuman/maping/server/internal/tenant"
)

func TestNewRowSortsSketchKeys(t *testing.T) {
	sketch := map[int32]uint64{5: 3, -2: 1, 0: 2, 100: 4}
	row := NewRow(
		tenant.MustParse("t"), "svc", "inst", "GET", "/x", "STATUS_CLASS_2XX",
		time.Unix(1, 0), time.Unix(2, 0),
		10, 0, 0, 0,
		sketch, nil,
	)

	require.Len(t, row.Sketch, 4)
	// Assert strictly ascending by index — deterministic map ordering.
	for i := 1; i < len(row.Sketch); i++ {
		assert.Less(t, row.Sketch[i-1].Index, row.Sketch[i].Index,
			"sketch buckets must be sorted ascending by index")
	}
	assert.Equal(t, []SketchBucket{
		{Index: -2, Count: 1},
		{Index: 0, Count: 2},
		{Index: 5, Count: 3},
		{Index: 100, Count: 4},
	}, row.Sketch)
}

func TestNewRowSortsStatusCodes(t *testing.T) {
	codes := map[uint32]uint64{500: 2, 200: 10, 404: 1}
	row := NewRow(
		tenant.MustParse("t"), "svc", "inst", "GET", "/x", "STATUS_CLASS_2XX",
		time.Unix(1, 0), time.Unix(2, 0),
		13, 0, 0, 0,
		nil, codes,
	)

	require.Len(t, row.StatusCodes, 3)
	for i := 1; i < len(row.StatusCodes); i++ {
		assert.Less(t, row.StatusCodes[i-1].Code, row.StatusCodes[i].Code)
	}
}

func TestRowMapRoundTrip(t *testing.T) {
	sketch := map[int32]uint64{1: 1, 2: 2}
	codes := map[uint32]uint64{200: 5}
	row := NewRow(
		tenant.MustParse("t"), "svc", "inst", "GET", "/x", "STATUS_CLASS_2XX",
		time.Unix(1, 0), time.Unix(2, 0),
		3, 0, 0, 0,
		sketch, codes,
	)
	assert.Equal(t, sketch, row.sketchMap())
	assert.Equal(t, codes, row.statusCodeMap())
}

func TestNewRowEmptyMaps(t *testing.T) {
	row := NewRow(
		tenant.MustParse("t"), "svc", "inst", "GET", "/x", "STATUS_CLASS_2XX",
		time.Unix(1, 0), time.Unix(2, 0),
		0, 0, 0, 0,
		nil, nil,
	)
	assert.Empty(t, row.Sketch)
	assert.Empty(t, row.StatusCodes)
	assert.Empty(t, row.sketchMap())
	assert.Empty(t, row.statusCodeMap())
}
