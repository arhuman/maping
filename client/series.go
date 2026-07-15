package maping

import (
	"strings"
	"sync"
	"time"

	mapingv1 "github.com/arhuman/maping/proto/maping/v1"

	"github.com/arhuman/maping/client/sketch"
)

// maxStatusCodes bounds the exact-code breakdown per series (top-N guard
// against cardinality blowups from odd codes).
const maxStatusCodes = 20

// maxErrorClasses bounds the distinct normalized error labels kept per series,
// mirroring maxStatusCodes: a new label past the cap is dropped rather than
// letting an unbounded set of error strings grow the map.
const maxErrorClasses = 20

// maxErrorClassLen caps a normalized error label to a short, storable token so a
// stray long error string cannot bloat the map key.
const maxErrorClassLen = 64

// NoStatusReason explains why a request finished without an HTTP status written
// (see classify → NO_STATUS). An adapter sets it on the Record; the Core records
// it only for NO_STATUS requests. The values mirror the proto NoStatusReason
// enum so the flush maps them straight through.
type NoStatusReason uint32

// The NoStatus* values enumerate why a request finished without an HTTP status.
// They mirror the proto NoStatusReason enum so the flush maps them straight
// through; NoStatusUnspecified is the zero value.
const (
	NoStatusUnspecified     NoStatusReason = 0
	NoStatusContextCanceled NoStatusReason = 1
	NoStatusContextDeadline NoStatusReason = 2
	NoStatusWriteError      NoStatusReason = 3
	NoStatusPanic           NoStatusReason = 4
	NoStatusOther           NoStatusReason = 5
)

// maxErrorExemplars bounds how many error breadcrumbs a window keeps per series,
// on top of the single slowest request. The reservoir cap is therefore K = 1
// (slowest) + maxErrorExemplars.
const maxErrorExemplars = 2

// Record is the neutral, framework-agnostic input to Observe. An adapter builds
// one from a completed request. TraceID/SpanID/RequestID are best-effort
// exemplar breadcrumbs: empty when the adapter cannot extract them.
type Record struct {
	Method        string
	RouteTemplate string
	Status        int
	Duration      time.Duration
	ReqBytes      int64
	RespBytes     int64
	TraceID       string
	SpanID        string
	RequestID     string
	// ErrorClass is an optional app/framework-supplied error label. The Core
	// normalizes it to uppercase [A-Z0-9_] and bounds the distinct set per series;
	// empty means the request carried no error label.
	ErrorClass string
	// NoStatusReason explains a NO_STATUS request (Status <= 0). It is ignored for
	// requests that did write a status.
	NoStatusReason NoStatusReason
	// DownstreamDuration is the time this request spent waiting on downstream calls
	// (outbound HTTP), captured by the maping RoundTripper. Zero when the host does
	// not wire it or the request made no downstream calls.
	DownstreamDuration time.Duration
}

// exemplar is one real request kept as a debugging breadcrumb. It is a value
// type stored inline in series, so recording one allocates nothing beyond the
// (already-allocated) id strings the adapter passed in.
type exemplar struct {
	atMs       int64
	durationNs uint64
	statusCode uint32
	traceID    string
	spanID     string
	requestID  string
}

// exemplarOf builds an exemplar breadcrumb from a completed Record, stamping the
// completion wall-clock. Status <= 0 (NO_STATUS) maps to code 0.
func exemplarOf(rec Record, now time.Time) exemplar {
	code := uint32(0)
	if rec.Status > 0 {
		code = uint32(rec.Status)
	}
	dur := uint64(0)
	if rec.Duration > 0 {
		dur = uint64(rec.Duration.Nanoseconds())
	}
	return exemplar{
		atMs:       now.UnixMilli(),
		durationNs: dur,
		statusCode: code,
		traceID:    rec.TraceID,
		spanID:     rec.SpanID,
		requestID:  rec.RequestID,
	}
}

// isError reports whether a status counts as an error for exemplar selection:
// 4xx, 5xx, or NO_STATUS (aborted before a status was written). Mirrors the
// error definition used everywhere else (docs/context.md).
func isError(status int) bool {
	return status <= 0 || status >= 400
}

// sanitizeErrorClass normalizes a raw error label into a bounded, storable token:
// uppercased, every run of non-[A-Z0-9_] bytes collapsed to a single underscore,
// no leading/trailing underscore, capped at maxErrorClassLen. It returns "" for
// an empty or fully-stripped input so the caller records nothing. This runs only
// for requests that carried an error label (the minority), so it never touches
// the 2xx hot path the alloc bench guards.
func sanitizeErrorClass(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	prevUnderscore := false
	for i := 0; i < len(s) && b.Len() < maxErrorClassLen; i++ {
		if u, ok := asciiUpperAlnum(s[i]); ok {
			b.WriteByte(u)
			prevUnderscore = false
		} else if b.Len() > 0 && !prevUnderscore {
			// Collapse a run of non-[A-Z0-9_] bytes to one underscore, and never
			// emit a leading one (b is still empty at the start).
			b.WriteByte('_')
			prevUnderscore = true
		}
	}
	// No trailing underscore; leading is already prevented above.
	return strings.TrimRight(b.String(), "_")
}

// asciiUpperAlnum returns c uppercased and true when c is an ASCII letter or
// digit, else (0, false) so sanitizeErrorClass collapses it to an underscore.
func asciiUpperAlnum(c byte) (byte, bool) {
	switch {
	case c >= 'a' && c <= 'z':
		return c - ('a' - 'A'), true
	case (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9'):
		return c, true
	default:
		return 0, false
	}
}

// recordErrorClass bumps a normalized error label, bounded to maxErrorClasses
// distinct labels. A new label past the cap is dropped rather than growing the
// map unbounded (mirrors recordCode).
func recordErrorClass(classes map[string]uint64, raw string) {
	label := sanitizeErrorClass(raw)
	if label == "" {
		return
	}
	if _, ok := classes[label]; !ok && len(classes) >= maxErrorClasses {
		return
	}
	classes[label]++
}

// recordNoStatusReason bumps the reason for a NO_STATUS request. The reason
// domain is a tiny fixed enum, so the map is inherently bounded; an out-of-range
// value is folded to NoStatusOther rather than stored as an unknown key.
func recordNoStatusReason(reasons map[uint32]uint64, r NoStatusReason) {
	if r > NoStatusOther {
		r = NoStatusOther
	}
	reasons[uint32(r)]++
}

// classify buckets an HTTP status into a StatusClass series-key dimension. A
// zero or sub-100 status is NO_STATUS (aborted before a status was written).
func classify(status int) mapingv1.StatusClass {
	switch {
	case status <= 0 || status < 100:
		return mapingv1.StatusClass_STATUS_CLASS_NO_STATUS
	case status < 300:
		return mapingv1.StatusClass_STATUS_CLASS_2XX
	case status < 400:
		return mapingv1.StatusClass_STATUS_CLASS_3XX
	case status < 500:
		return mapingv1.StatusClass_STATUS_CLASS_4XX
	default:
		return mapingv1.StatusClass_STATUS_CLASS_5XX
	}
}

// seriesKey identifies one time series within a flush window. Service and
// instance are fixed per recorder and live on the Envelope, not the key.
type seriesKey struct {
	method string
	route  string
	class  mapingv1.StatusClass
}

// series is the in-window aggregate for one seriesKey.
type series struct {
	count           uint64
	sumDurationNs   uint64
	reqBytes        uint64
	respBytes       uint64
	maxDurationNs   uint64 // exact slowest request in the window (O(1) max).
	sk              *sketch.DDSketch
	codes           map[uint32]uint64 // bounded top-N exact codes
	errorClasses    map[string]uint64 // bounded top-N normalized error labels
	noStatusReasons map[uint32]uint64 // reasons for NO_STATUS requests (small enum domain)
	sumDownstreamNs uint64            // summed downstream (outbound) wait time

	// Bounded exemplar reservoir (cap K = 1 + maxErrorExemplars). slowest keeps
	// the single slowest request ever seen this window; errs keeps the FIRST few
	// error requests. Both are inline value arrays, so a non-exemplar request
	// (not the new slowest, not an early error) records nothing here.
	slowest    exemplar
	hasSlowest bool
	errs       [maxErrorExemplars]exemplar
	nErrs      int
}

// observeExemplar folds one completed request into the series' max-duration and
// bounded exemplar reservoir. It is O(1) and allocates nothing: the slowest slot
// is overwritten in place only when this request is slower, and an error slot is
// filled only while fewer than maxErrorExemplars errors have been seen. ex is
// built by the caller (once) so the wall-clock is stamped at observation time.
func (s *series) observeExemplar(rec Record, ex exemplar) {
	if ex.durationNs > s.maxDurationNs {
		s.maxDurationNs = ex.durationNs
	}
	if !s.hasSlowest || ex.durationNs > s.slowest.durationNs {
		s.slowest = ex
		s.hasSlowest = true
	}
	if isError(rec.Status) && s.nErrs < maxErrorExemplars {
		s.errs[s.nErrs] = ex
		s.nErrs++
	}
}

// exemplars flattens the reservoir into the proto wire form for flush. It emits
// the slowest request (if any) first, then the kept error breadcrumbs, skipping
// an error that is the same object as the slowest so a single slow-error request
// is not listed twice. The result is bounded by K.
func (s *series) exemplars() []*mapingv1.Exemplar {
	if !s.hasSlowest && s.nErrs == 0 {
		return nil
	}
	out := make([]*mapingv1.Exemplar, 0, 1+s.nErrs)
	if s.hasSlowest {
		out = append(out, s.slowest.toProto())
	}
	for i := 0; i < s.nErrs; i++ {
		if s.hasSlowest && s.errs[i] == s.slowest {
			continue
		}
		out = append(out, s.errs[i].toProto())
	}
	return out
}

// toProto converts an exemplar breadcrumb to its wire form.
func (e exemplar) toProto() *mapingv1.Exemplar {
	return &mapingv1.Exemplar{
		AtMs:       e.atMs,
		DurationNs: e.durationNs,
		StatusCode: e.statusCode,
		TraceId:    e.traceID,
		SpanId:     e.spanID,
		RequestId:  e.requestID,
	}
}

// shard is one lock-partitioned slice of the aggregation map. Observe touches
// only the shard its seriesKey hashes to, so N shards let up to N requests
// aggregate concurrently without contending on a single mutex.
type shard struct {
	mu sync.Mutex
	m  map[seriesKey]*series
}

// hash is a small FNV-1a over the seriesKey fields, computed without allocation
// so the hot path stays alloc-free. It ranges over the strings byte-by-byte
// (no []byte conversion) and folds in the status class.
func (k seriesKey) hash() uint32 {
	const (
		offset uint32 = 2166136261
		prime  uint32 = 16777619
	)
	h := offset
	for i := 0; i < len(k.method); i++ {
		h = (h ^ uint32(k.method[i])) * prime
	}
	for i := 0; i < len(k.route); i++ {
		h = (h ^ uint32(k.route[i])) * prime
	}
	c := uint32(k.class)
	h = (h ^ (c & 0xff)) * prime
	h = (h ^ ((c >> 8) & 0xff)) * prime
	return h
}
