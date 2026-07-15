package maping

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mapingv1 "github.com/arhuman/maping/proto/maping/v1"
)

// obs is a tiny helper: fold a Record into a series via the same path Observe
// uses (max + reservoir), stamping a fixed wall-clock so at_ms is deterministic.
func obs(s *series, rec Record) {
	s.observeExemplar(rec, exemplarOf(rec, time.UnixMilli(1_700_000_000_000)))
}

func TestSeriesTracksMaxDuration(t *testing.T) {
	var s series
	obs(&s, Record{Status: 200, Duration: 10 * time.Millisecond})
	obs(&s, Record{Status: 200, Duration: 40 * time.Millisecond})
	obs(&s, Record{Status: 200, Duration: 25 * time.Millisecond})

	assert.Equal(t, uint64((40 * time.Millisecond).Nanoseconds()), s.maxDurationNs,
		"maxDurationNs must be the exact slowest request in the window")
}

func TestSeriesReservoirKeepsSlowest(t *testing.T) {
	var s series
	obs(&s, Record{Status: 200, Duration: 10 * time.Millisecond, RequestID: "a"})
	obs(&s, Record{Status: 200, Duration: 50 * time.Millisecond, RequestID: "slow"})
	obs(&s, Record{Status: 200, Duration: 20 * time.Millisecond, RequestID: "b"})

	require.True(t, s.hasSlowest)
	assert.Equal(t, "slow", s.slowest.requestID, "reservoir must keep the single slowest request")
	assert.Equal(t, uint64((50 * time.Millisecond).Nanoseconds()), s.slowest.durationNs)

	// Only the slowest exemplar (no errors), so exactly one is emitted.
	ex := s.exemplars()
	require.Len(t, ex, 1)
	assert.Equal(t, "slow", ex[0].GetRequestId())
}

func TestSeriesReservoirKeepsFirstErrors(t *testing.T) {
	var s series
	// Two successes, then several errors. Only the FIRST maxErrorExemplars errors
	// are kept; later ones are dropped.
	obs(&s, Record{Status: 200, Duration: time.Millisecond, RequestID: "ok"})
	obs(&s, Record{Status: 500, Duration: 2 * time.Millisecond, RequestID: "err1"})
	obs(&s, Record{Status: 404, Duration: 3 * time.Millisecond, RequestID: "err2"})
	obs(&s, Record{Status: 503, Duration: 4 * time.Millisecond, RequestID: "err3-dropped"})

	require.Equal(t, maxErrorExemplars, s.nErrs, "error reservoir must be capped at maxErrorExemplars")
	assert.Equal(t, "err1", s.errs[0].requestID)
	assert.Equal(t, "err2", s.errs[1].requestID)
}

func TestSeriesReservoirCapEnforced(t *testing.T) {
	var s series
	// The slowest request is a success; two distinct errors follow. The wire
	// output is capped at K = 1 (slowest) + maxErrorExemplars.
	obs(&s, Record{Status: 200, Duration: 100 * time.Millisecond, RequestID: "slow-ok"})
	obs(&s, Record{Status: 500, Duration: time.Millisecond, RequestID: "e1"})
	obs(&s, Record{Status: 500, Duration: time.Millisecond, RequestID: "e2"})
	obs(&s, Record{Status: 500, Duration: time.Millisecond, RequestID: "e3"})

	ex := s.exemplars()
	require.LessOrEqual(t, len(ex), 1+maxErrorExemplars, "exemplar output must be bounded by K")
	require.Len(t, ex, 3)
	assert.Equal(t, "slow-ok", ex[0].GetRequestId(), "slowest is emitted first")
	assert.Equal(t, "e1", ex[1].GetRequestId())
	assert.Equal(t, "e2", ex[2].GetRequestId())
}

func TestSeriesNoStatusCountsAsError(t *testing.T) {
	var s series
	obs(&s, Record{Status: 0, Duration: time.Millisecond, RequestID: "aborted"})
	require.Equal(t, 1, s.nErrs, "NO_STATUS (status <= 0) must count as an error exemplar")
	assert.Equal(t, uint32(0), s.errs[0].statusCode, "aborted request maps to status code 0")
}

func TestSeriesSlowErrorNotDuplicated(t *testing.T) {
	var s series
	// A single slow error is both the slowest AND an error; it must appear once.
	obs(&s, Record{Status: 500, Duration: 90 * time.Millisecond, RequestID: "slow-err"})
	obs(&s, Record{Status: 200, Duration: time.Millisecond, RequestID: "ok"})

	ex := s.exemplars()
	require.Len(t, ex, 1, "a slow-error request must not be listed twice")
	assert.Equal(t, "slow-err", ex[0].GetRequestId())
}

func TestSeriesExemplarProtoFields(t *testing.T) {
	var s series
	obs(&s, Record{
		Status: 503, Duration: 7 * time.Millisecond,
		TraceID: "4bf92f3577b34da6a3ce929d0e0e4736", SpanID: "00f067aa0ba902b7",
		RequestID: "req-1",
	})
	ex := s.exemplars()
	require.Len(t, ex, 1)
	got := ex[0]
	assert.Equal(t, uint32(503), got.GetStatusCode())
	assert.Equal(t, uint64((7 * time.Millisecond).Nanoseconds()), got.GetDurationNs())
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", got.GetTraceId())
	assert.Equal(t, "00f067aa0ba902b7", got.GetSpanId())
	assert.Equal(t, "req-1", got.GetRequestId())
	assert.Equal(t, int64(1_700_000_000_000), got.GetAtMs())
}

func TestSeriesEmptyReservoir(t *testing.T) {
	var s series
	assert.Nil(t, s.exemplars(), "an empty window must emit no exemplars")
}

// TestObserveEmitsMaxAndExemplars is an end-to-end check that Observe feeds the
// max and reservoir through into the built Summary.
func TestObserveEmitsMaxAndExemplars(t *testing.T) {
	r := newTestRecorder(&fakeTransport{})
	r.Observe(Record{Method: "GET", RouteTemplate: "/x", Status: 200, Duration: 5 * time.Millisecond})
	r.Observe(Record{Method: "GET", RouteTemplate: "/x", Status: 200, Duration: 50 * time.Millisecond, RequestID: "slow"})

	req := r.buildRequest(r.swapShards(), time.Now())
	require.Len(t, req.Summaries, 1)
	sum := req.Summaries[0]
	assert.Equal(t, uint64((50 * time.Millisecond).Nanoseconds()), sum.GetMaxDurationNs())
	require.Len(t, sum.GetExemplars(), 1)
	assert.Equal(t, "slow", sum.GetExemplars()[0].GetRequestId())
	assert.Equal(t, mapingv1.StatusClass_STATUS_CLASS_2XX, sum.GetStatusClass())
}

func TestSanitizeErrorClass(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"db pool exhausted", "DB_POOL_EXHAUSTED"},
		{"connection refused: 10.0.0.5:5432", "CONNECTION_REFUSED_10_0_0_5_5432"},
		{"already_upper", "ALREADY_UPPER"},
		{"   leading and trailing   ", "LEADING_AND_TRAILING"},
		{"!!!", ""}, // all-symbol collapses to nothing
	}
	for _, c := range cases {
		assert.Equal(t, c.want, sanitizeErrorClass(c.in), "sanitizeErrorClass(%q)", c.in)
	}
	// Cap at maxErrorClassLen (no trailing underscore left by truncation).
	long := sanitizeErrorClass(strings.Repeat("a", maxErrorClassLen+50))
	assert.LessOrEqual(t, len(long), maxErrorClassLen)
	assert.Equal(t, strings.Repeat("A", maxErrorClassLen), long)
}

func TestRecordErrorClassBounded(t *testing.T) {
	classes := make(map[string]uint64)
	// Fill to the cap with distinct labels, then a new one past the cap is dropped
	// while an existing label keeps counting.
	for i := 0; i < maxErrorClasses; i++ {
		recordErrorClass(classes, "err"+strconv.Itoa(i))
	}
	require.Len(t, classes, maxErrorClasses)
	recordErrorClass(classes, "one-too-many")
	assert.Len(t, classes, maxErrorClasses, "a new label past the cap is dropped")
	recordErrorClass(classes, "err0")
	assert.Equal(t, uint64(2), classes["ERR0"], "an existing label keeps counting past the cap")
}

func TestRecordNoStatusReasonClampsUnknown(t *testing.T) {
	reasons := make(map[uint32]uint64)
	recordNoStatusReason(reasons, NoStatusContextDeadline)
	recordNoStatusReason(reasons, NoStatusReason(99)) // out of range → folded to OTHER
	assert.Equal(t, uint64(1), reasons[uint32(NoStatusContextDeadline)])
	assert.Equal(t, uint64(1), reasons[uint32(NoStatusOther)])
}

// TestObserveEmitsErrorSignatures checks Observe threads the error label and the
// NO_STATUS reason through into the built Summary's bounded maps.
func TestObserveEmitsErrorSignatures(t *testing.T) {
	r := newTestRecorder(&fakeTransport{})
	r.Observe(Record{Method: "GET", RouteTemplate: "/x", Status: 500, Duration: time.Millisecond, ErrorClass: "db timeout"})
	r.Observe(Record{Method: "GET", RouteTemplate: "/x", Status: 0, Duration: time.Millisecond, NoStatusReason: NoStatusContextCanceled})

	req := r.buildRequest(r.swapShards(), time.Now())
	byClass := map[mapingv1.StatusClass]*mapingv1.Summary{}
	for _, s := range req.Summaries {
		byClass[s.GetStatusClass()] = s
	}
	if s := byClass[mapingv1.StatusClass_STATUS_CLASS_5XX]; assert.NotNil(t, s) {
		assert.Equal(t, uint64(1), s.GetErrorClassBreakdown()["DB_TIMEOUT"])
	}
	if s := byClass[mapingv1.StatusClass_STATUS_CLASS_NO_STATUS]; assert.NotNil(t, s) {
		assert.Equal(t, uint64(1), s.GetNoStatusReasons()[uint32(NoStatusContextCanceled)])
	}
}
