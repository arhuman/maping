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

// ErrorClass is one normalized error-label count for the error_classes map
// column. Class is an uppercase [A-Z0-9_] token bounded by the client.
type ErrorClass struct {
	Class string
	Count uint64
}

// NoStatusReason is one reason count for the no_status_reasons map column.
// Reason is the proto NoStatusReason enum value, stored as UInt8 (the domain is
// a tiny fixed enum).
type NoStatusReason struct {
	Reason uint8
	Count  uint64
}

// Exemplar is one real-request breadcrumb attached to a Row. It is stored only
// in the raw summaries tier (rollups drop it) and lets a user pivot from a p99 /
// error spike to an actual request. TraceID/SpanID/RequestID are best-effort and
// may be empty. The tuple column order in ClickHouse is
// (at, duration_ns, status_code, trace_id, span_id, request_id).
type Exemplar struct {
	At         time.Time
	DurationNs uint64
	StatusCode uint32
	TraceID    string
	SpanID     string
	RequestID  string
}

// Row is one Summary ready for insertion: the server-resolved tenant plus the
// full series key, RED counters, and the merged-at-query DDSketch buckets. The
// sketch keys are pre-sorted (see NewRow) for deterministic map ordering.
type Row struct {
	Tenant          tenant.ID
	Service         string
	Instance        string
	Method          string
	RouteTemplate   string
	StatusClass     string // Enum8 string value, e.g. "STATUS_CLASS_2XX".
	WindowStart     time.Time
	WindowEnd       time.Time
	Count           uint64
	SumDurationNs   uint64
	ReqBytes        uint64
	RespBytes       uint64
	Sketch          []SketchBucket   // sorted ascending by Index.
	StatusCodes     []StatusCode     // sorted ascending by Code.
	MaxDurationNs   uint64           // exact slowest request in the window; merges via max.
	Exemplars       []Exemplar       // bounded breadcrumbs; raw tier only.
	ErrorClasses    []ErrorClass     // sorted ascending by Class; merges via sumMap.
	NoStatusReasons []NoStatusReason // sorted ascending by Reason; merges via sumMap.
	SumDownstreamNs uint64           // summed downstream wait time; merges via sum.

	// Deploy identity: stored low-cardinality dimensions carried from the
	// Envelope. DeployVersion is a sort-key dimension (rows from different
	// versions never collapse); the rest are non-key stored columns.
	// InstanceStart is the process boot wall-clock (zero when the client did not
	// report one).
	DeployVersion string
	DeployID      string
	Environment   string
	Region        string
	InstanceStart time.Time
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
	deployVersion, deployID, environment, region string,
	instanceStart time.Time,
	maxDurationNs uint64,
	exemplars []Exemplar,
	errorClasses map[string]uint64,
	noStatusReasons map[uint32]uint64,
	sumDownstreamNs uint64,
) Row {
	return Row{
		Tenant:          tenantID,
		Service:         service,
		Instance:        instance,
		Method:          method,
		RouteTemplate:   routeTemplate,
		StatusClass:     statusClass,
		WindowStart:     windowStart,
		WindowEnd:       windowEnd,
		Count:           count,
		SumDurationNs:   sumDurationNs,
		ReqBytes:        reqBytes,
		RespBytes:       respBytes,
		Sketch:          sortedSketch(sketch),
		StatusCodes:     sortedStatusCodes(statusCodes),
		DeployVersion:   deployVersion,
		DeployID:        deployID,
		Environment:     environment,
		Region:          region,
		InstanceStart:   instanceStart,
		MaxDurationNs:   maxDurationNs,
		Exemplars:       exemplars,
		ErrorClasses:    sortedErrorClasses(errorClasses),
		NoStatusReasons: sortedNoStatusReasons(noStatusReasons),
		SumDownstreamNs: sumDownstreamNs,
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

// sortedErrorClasses converts a normalized error-label count map into a slice
// sorted ascending by label, so the ClickHouse map column insert order is
// deterministic.
//
//nolint:dupl // thin parallel wrapper over sortedByKey; the shared loop+sort is already deduplicated there.
func sortedErrorClasses(m map[string]uint64) []ErrorClass {
	return sortedByKey(m,
		func(class string, count uint64) ErrorClass { return ErrorClass{Class: class, Count: count} },
		func(e ErrorClass) string { return e.Class })
}

// sortedNoStatusReasons converts a reason count map (keyed by the proto enum
// value as uint32) into a slice sorted ascending by reason, narrowing the key to
// the UInt8 storage domain.
func sortedNoStatusReasons(m map[uint32]uint64) []NoStatusReason {
	out := make([]NoStatusReason, 0, len(m))
	for k, count := range m {
		out = append(out, NoStatusReason{Reason: uint8(k), Count: count})
	}
	slices.SortFunc(out, func(a, b NoStatusReason) int { return cmp.Compare(a.Reason, b.Reason) })
	return out
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

// exemplarTuples rebuilds the exemplars as a slice of positional tuples for the
// CH driver, which takes Array(Tuple(...)) as [][]any. The element order MUST
// match the tuple column order declared on the raw summaries table:
// (at, duration_ns, status_code, trace_id, span_id, request_id).
func (r Row) exemplarTuples() [][]any {
	out := make([][]any, 0, len(r.Exemplars))
	for _, e := range r.Exemplars {
		out = append(out, []any{
			e.At, e.DurationNs, e.StatusCode, e.TraceID, e.SpanID, e.RequestID,
		})
	}
	return out
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

// errorClassMap rebuilds the map form of the sorted error classes for the CH
// driver (Map(String,UInt64)).
func (r Row) errorClassMap() map[string]uint64 {
	m := make(map[string]uint64, len(r.ErrorClasses))
	for _, e := range r.ErrorClasses {
		m[e.Class] = e.Count
	}
	return m
}

// noStatusReasonMap rebuilds the map form of the sorted no-status reasons for the
// CH driver (Map(UInt8,UInt64)).
func (r Row) noStatusReasonMap() map[uint8]uint64 {
	m := make(map[uint8]uint64, len(r.NoStatusReasons))
	for _, n := range r.NoStatusReasons {
		m[n.Reason] = n.Count
	}
	return m
}
