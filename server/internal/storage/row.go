package storage

import (
	"cmp"
	"slices"
	"time"

	"github.com/arhuman/maping/server/internal/tenant"
)

// SketchBucket is one DDSketch bucket: an index and its count. Rows carry the
// sketch as a sorted slice (not a map) so the map column insert order into
// ClickHouse is deterministic.
type SketchBucket struct {
	Index int32
	Count uint64
}

// StatusCode is one exact-code count for the status_codes map column.
type StatusCode struct {
	Code  uint32
	Count uint64
}

// Row is one Summary ready for insertion: the server-resolved tenant plus the
// full series key, RED counters, and the merged-at-query DDSketch buckets. The
// sketch keys are pre-sorted (see NewRow) for deterministic map ordering.
type Row struct {
	Tenant        tenant.ID
	Service       string
	Instance      string
	Method        string
	RouteTemplate string
	StatusClass   string // Enum8 string value, e.g. "STATUS_CLASS_2XX".
	WindowStart   time.Time
	WindowEnd     time.Time
	Count         uint64
	SumDurationNs uint64
	ReqBytes      uint64
	RespBytes     uint64
	Sketch        []SketchBucket // sorted ascending by Index.
	StatusCodes   []StatusCode   // sorted ascending by Code.
}

// NewRow builds a Row from an already-mapped sketch and status-code maps,
// sorting both by key so the ClickHouse map column ordering is deterministic
// across inserts.
func NewRow(
	tenantID tenant.ID,
	service, instance, method, routeTemplate, statusClass string,
	windowStart, windowEnd time.Time,
	count, sumDurationNs, reqBytes, respBytes uint64,
	sketch map[int32]uint64,
	statusCodes map[uint32]uint64,
) Row {
	return Row{
		Tenant:        tenantID,
		Service:       service,
		Instance:      instance,
		Method:        method,
		RouteTemplate: routeTemplate,
		StatusClass:   statusClass,
		WindowStart:   windowStart,
		WindowEnd:     windowEnd,
		Count:         count,
		SumDurationNs: sumDurationNs,
		ReqBytes:      reqBytes,
		RespBytes:     respBytes,
		Sketch:        sortedSketch(sketch),
		StatusCodes:   sortedStatusCodes(statusCodes),
	}
}

// sortedByKey flattens a count map into a slice built by elem and sorted
// ascending by key, so the ClickHouse map column insert order is deterministic.
// It backs both the sketch-bucket and status-code conversions.
func sortedByKey[K cmp.Ordered, V any](m map[K]uint64, elem func(key K, count uint64) V, keyOf func(V) K) []V {
	out := make([]V, 0, len(m))
	for k, count := range m {
		out = append(out, elem(k, count))
	}
	slices.SortFunc(out, func(a, b V) int { return cmp.Compare(keyOf(a), keyOf(b)) })
	return out
}

// sortedSketch converts a DDSketch bucket map into a slice sorted ascending by
// index.
//
//nolint:dupl // thin parallel wrapper over sortedByKey; the shared loop+sort is already deduplicated there.
func sortedSketch(m map[int32]uint64) []SketchBucket {
	return sortedByKey(m,
		func(index int32, count uint64) SketchBucket { return SketchBucket{Index: index, Count: count} },
		func(b SketchBucket) int32 { return b.Index })
}

// sortedStatusCodes converts an exact-code count map into a slice sorted
// ascending by code.
//
//nolint:dupl // thin parallel wrapper over sortedByKey; the shared loop+sort is already deduplicated there.
func sortedStatusCodes(m map[uint32]uint64) []StatusCode {
	return sortedByKey(m,
		func(code uint32, count uint64) StatusCode { return StatusCode{Code: code, Count: count} },
		func(s StatusCode) uint32 { return s.Code })
}

// sketchMap rebuilds the map form of the sorted sketch for the CH driver, which
// takes Map(Int32,UInt64) as a Go map. Insertion order into the map is
// irrelevant to ClickHouse once inside the driver, but the slice ordering
// upstream keeps the pipeline deterministic and testable.
func (r Row) sketchMap() map[int32]uint64 {
	m := make(map[int32]uint64, len(r.Sketch))
	for _, b := range r.Sketch {
		m[b.Index] = b.Count
	}
	return m
}

// statusCodeMap rebuilds the map form of the sorted status codes for the CH
// driver.
func (r Row) statusCodeMap() map[uint32]uint64 {
	m := make(map[uint32]uint64, len(r.StatusCodes))
	for _, s := range r.StatusCodes {
		m[s.Code] = s.Count
	}
	return m
}
