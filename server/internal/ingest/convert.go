package ingest

import (
	"fmt"
	"time"

	"github.com/arhuman/maping/server/internal/storage"
	"github.com/arhuman/maping/server/internal/tenant"

	mapingv1 "github.com/arhuman/maping/proto/maping/v1"
)

// skewTolerance is the timestamp drift band. Summaries whose window_end is
// within this of server-now are accepted (small drift clamped into range);
// beyond it the summary is dropped and counted rather than clamped onto now,
// which would corrupt the live pane.
const skewTolerance = 10 * time.Minute

// statusClassName maps the proto StatusClass enum to the ClickHouse Enum8
// string value. Unspecified (proto3 default-zero, "never sent") is preserved as
// its own bucket rather than silently coerced, so bad clients are visible.
func statusClassName(sc mapingv1.StatusClass) string {
	switch sc {
	case mapingv1.StatusClass_STATUS_CLASS_2XX:
		return "STATUS_CLASS_2XX"
	case mapingv1.StatusClass_STATUS_CLASS_3XX:
		return "STATUS_CLASS_3XX"
	case mapingv1.StatusClass_STATUS_CLASS_4XX:
		return "STATUS_CLASS_4XX"
	case mapingv1.StatusClass_STATUS_CLASS_5XX:
		return "STATUS_CLASS_5XX"
	case mapingv1.StatusClass_STATUS_CLASS_NO_STATUS:
		return "STATUS_CLASS_NO_STATUS"
	default:
		return "STATUS_CLASS_UNSPECIFIED"
	}
}

// timestampDecision is the outcome of applying the skew policy to one summary.
type timestampDecision struct {
	// accepted is true when the summary is within the tolerance band.
	accepted bool
	// start/end are the timestamps to store when accepted. Small in-band drift
	// is kept as-is (no clamping needed inside the band).
	start time.Time
	end   time.Time
}

// applyTimestampPolicy decides whether a summary's window is acceptable given
// the server's current time. A summary is accepted when |window_end - now| <=
// skewTolerance; otherwise it is dropped and counted.
func applyTimestampPolicy(startMs, endMs int64, now time.Time) timestampDecision {
	end := time.UnixMilli(endMs).UTC()
	drift := now.Sub(end)
	if drift < 0 {
		drift = -drift
	}
	if drift > skewTolerance {
		return timestampDecision{accepted: false}
	}
	return timestampDecision{
		accepted: true,
		start:    time.UnixMilli(startMs).UTC(),
		end:      end,
	}
}

// summaryToRow converts one accepted proto Summary into a storage.Row, stamping
// the resolved tenant and envelope-derived service/instance plus the deploy
// identity (deployVersion/deployID/environment/region/instanceStart). The deploy
// fields come from the Envelope and so apply to every summary in the batch. The
// caller must have already applied the timestamp policy; start/end come from the
// decision.
func summaryToRow(
	tid tenant.ID,
	service, instance string,
	deployVersion, deployID, environment, region string,
	instanceStart time.Time,
	s *mapingv1.Summary,
	start, end time.Time,
) (storage.Row, error) {
	if s == nil {
		return storage.Row{}, fmt.Errorf("ingest.summaryToRow: nil summary")
	}
	return storage.NewRow(
		tid, service, instance,
		s.GetMethod(), s.GetRouteTemplate(), statusClassName(s.GetStatusClass()),
		start, end,
		s.GetCount(), s.GetSumDurationNs(), s.GetReqBytes(), s.GetRespBytes(),
		s.GetLatencySketch(), s.GetStatusCodeBreakdown(),
		deployVersion, deployID, environment, region, instanceStart,
		s.GetMaxDurationNs(), exemplarsToRows(s.GetExemplars()),
		s.GetErrorClassBreakdown(), s.GetNoStatusReasons(),
		s.GetSumDownstreamDurationNs(),
	), nil
}

// instanceWindowToRow converts one proto InstanceWindow into a storage row,
// applying the same timestamp-skew policy as summaries (an out-of-band window is
// dropped, not clamped). service/instance come from the Envelope. The bool is
// false when the window is nil or skew-rejected, so the caller can skip it.
func instanceWindowToRow(
	tid tenant.ID,
	service, instance string,
	iw *mapingv1.InstanceWindow,
	now time.Time,
) (storage.InstanceWindowRow, bool) {
	if iw == nil {
		return storage.InstanceWindowRow{}, false
	}
	decision := applyTimestampPolicy(iw.GetWindowStartMs(), iw.GetWindowEndMs(), now)
	if !decision.accepted {
		return storage.InstanceWindowRow{}, false
	}
	return storage.InstanceWindowRow{
		Tenant:         tid,
		Service:        service,
		Instance:       instance,
		WindowStart:    decision.start,
		WindowEnd:      decision.end,
		CPUNs:          iw.GetCpuNs(),
		RSSBytes:       iw.GetRssBytes(),
		HeapAllocBytes: iw.GetHeapAllocBytes(),
		GCPauseNs:      iw.GetGcPauseNs(),
		Goroutines:     iw.GetGoroutines(),
	}, true
}

// exemplarsToRows converts proto exemplar breadcrumbs into storage.Exemplar,
// mapping at_ms to a UTC time.Time. It returns nil for an empty input so a
// summary without exemplars stores an empty array.
func exemplarsToRows(exs []*mapingv1.Exemplar) []storage.Exemplar {
	if len(exs) == 0 {
		return nil
	}
	out := make([]storage.Exemplar, 0, len(exs))
	for _, e := range exs {
		if e == nil {
			continue
		}
		out = append(out, storage.Exemplar{
			At:         time.UnixMilli(e.GetAtMs()).UTC(),
			DurationNs: e.GetDurationNs(),
			StatusCode: e.GetStatusCode(),
			TraceID:    e.GetTraceId(),
			SpanID:     e.GetSpanId(),
			RequestID:  e.GetRequestId(),
		})
	}
	return out
}
