package web

import (
	"sort"
	"strconv"

	"github.com/arhuman/maping/server/internal/storage"
)

// statusClassView is one class row in the endpoint-detail breakdown.
type statusClassView struct {
	Class   string
	Count   uint64
	IsError bool // 4xx/5xx/no_status count toward the headline error rate.
}

// statusCodeView is one exact-code row, sorted ascending by code for a stable
// display.
type statusCodeView struct {
	Code  uint32
	Count uint64
}

// detailView is the rendered endpoint-detail headline: RED numbers plus the
// class breakdown and exact-code table shown beside the error rate.
type detailView struct {
	Count     uint64
	ErrorRate float64
	ErrorHigh bool
	P50       float64
	P95       float64
	P99       float64
	Classes   []statusClassView
	Codes     []statusCodeView
}

// errorClasses is the set of status classes that count toward the headline error
// rate, matching the CONTEXT Error definition.
var errorClasses = map[string]bool{
	"4xx":       true,
	"5xx":       true,
	"no_status": true,
}

// toDetailView maps the storage EndpointDetail into the rendered headline view,
// marking which classes are errors and sorting the exact codes.
func toDetailView(d storage.EndpointDetail) detailView {
	v := detailView{
		Count:     d.Count,
		ErrorRate: d.ErrorRate,
		ErrorHigh: d.ErrorRate >= errorRateWarn,
		P50:       d.P50,
		P95:       d.P95,
		P99:       d.P99,
	}
	for _, c := range d.StatusClasses {
		v.Classes = append(v.Classes, statusClassView{
			Class:   c.Class,
			Count:   c.Count,
			IsError: errorClasses[c.Class],
		})
	}
	for code, count := range d.StatusCodes {
		v.Codes = append(v.Codes, statusCodeView{Code: code, Count: count})
	}
	sort.Slice(v.Codes, func(i, j int) bool { return v.Codes[i].Code < v.Codes[j].Code })
	return v
}

// instanceStatRow is one rendered row of the endpoint-detail instance-outlier
// panel: a single replica's RED metrics plus its average payload sizes. Rows
// arrive already ordered by instance from storage, so no re-sort is needed.
type instanceStatRow struct {
	Instance     string
	Count        uint64
	ErrorRate    float64
	ErrorHigh    bool
	P50          float64
	P95          float64
	P99          float64
	ReqBytesAvg  float64
	RespBytesAvg float64
	IsOutlier    bool // p95 discriminates this replica from the fleet (see toInstanceRows)
}

// p95OutlierFloor is the absolute latency floor (100ms) a replica's p95 must clear
// before it can be flagged an outlier, so a fleet of fast replicas never flags a
// still-fast one purely on a relative multiple.
const p95OutlierFloor = 0.1

// toInstanceRows maps the storage instance-outlier stats into display rows,
// preserving the storage order (by instance). It flags a replica IsOutlier when the
// fleet has at least two trafficked instances and this row's p95 is both at least
// twice the fleet median p95 and above the 100ms floor — the signal that a
// degradation is localized to one replica rather than fleet-wide.
func toInstanceRows(stats []storage.InstanceStat) []instanceStatRow {
	var p95s []float64
	for _, s := range stats {
		if s.Count > 0 {
			p95s = append(p95s, s.P95)
		}
	}
	var med float64
	if len(p95s) > 0 {
		sorted := make([]float64, len(p95s))
		copy(sorted, p95s)
		sort.Float64s(sorted)
		med = median(sorted)
	}
	out := make([]instanceStatRow, 0, len(stats))
	for _, s := range stats {
		isOutlier := len(p95s) >= 2 && s.P95 >= 2*med && s.P95 >= p95OutlierFloor
		out = append(out, instanceStatRow{
			Instance:     s.Instance,
			Count:        s.Count,
			ErrorRate:    s.ErrorRate,
			ErrorHigh:    s.ErrorRate >= errorRateWarn,
			P50:          s.P50,
			P95:          s.P95,
			P99:          s.P99,
			ReqBytesAvg:  s.ReqBytesAvg,
			RespBytesAvg: s.RespBytesAvg,
			IsOutlier:    isOutlier,
		})
	}
	return out
}

// versionRow is one rendered row of the endpoint-detail deploy-version panel: a
// single deploy_version's RED metrics over the window. Rows arrive already
// ordered by version from storage, so no re-sort is needed. It answers "did a
// release regress this endpoint?" by putting each version's metrics side by side.
type versionRow struct {
	Version   string
	Count     uint64
	ErrorRate float64
	ErrorHigh bool
	P50       float64
	P95       float64
	P99       float64
}

// toVersionRows maps the storage per-deploy-version stats into display rows,
// preserving the storage order (by version). The empty/unknown version is
// dropped so a service that never sets deploy_version falls through to the
// panel's empty state instead of showing a blank version row.
func toVersionRows(stats []storage.VersionStat) []versionRow {
	out := make([]versionRow, 0, len(stats))
	for _, s := range stats {
		if s.Version == "" {
			continue
		}
		out = append(out, versionRow{
			Version:   s.Version,
			Count:     s.Count,
			ErrorRate: s.ErrorRate,
			ErrorHigh: s.ErrorRate >= errorRateWarn,
			P50:       s.P50,
			P95:       s.P95,
			P99:       s.P99,
		})
	}
	return out
}

// dominantVersion returns the deploy_version with the most requests in the
// window, skipping the empty/unknown version, or "" when there is no usable
// version data. It drives the DEBUG CONTEXT release annotation.
func dominantVersion(stats []storage.VersionStat) string {
	best := ""
	var bestCount uint64
	for _, s := range stats {
		if s.Version == "" {
			continue
		}
		if s.Count > bestCount {
			best, bestCount = s.Version, s.Count
		}
	}
	return best
}

// exemplarRow is one rendered row of the endpoint-detail exemplars panel: a real
// captured request, letting an operator pivot from a spike to an actual trace. It
// keeps both the display-truncated trace/request ids (Short*) and the full values
// (Full*) so the row shows a compact monospace id but copies the whole thing.
type exemplarRow struct {
	Time       string  // compact HH:MM:SS (UTC) of the captured request
	StatusCode uint32  // exact status code, coloured by class in the template
	Latency    float64 // request duration in seconds (formatted via msf)
	FullTrace  string  // full trace id ("" when the exemplar carried none)
	ShortTrace string  // display-truncated trace id, or "" when none
	FullReq    string  // full request id ("" when the exemplar carried none)
	ShortReq   string  // display-truncated request id, or "" when none
}

// exemplarTimeLayout renders an exemplar timestamp compactly in UTC: the operator
// scans a window of at most an hour, so a wall-clock time is enough to correlate.
const exemplarTimeLayout = "15:04:05"

// idTruncLen bounds how much of a trace/request id is shown in the exemplars
// table; the full value is preserved on the row for copy.
const idTruncLen = 12

// truncID shortens an id for display, appending an ellipsis when it was cut. An
// empty id returns "" so the template can render an em-dash instead.
func truncID(id string) string {
	if id == "" {
		return ""
	}
	if len(id) <= idTruncLen {
		return id
	}
	return id[:idTruncLen] + "…"
}

// toExemplarRows maps the storage exemplar breadcrumbs into display rows,
// preserving the storage order (latency descending). Duration is converted from
// nanoseconds to seconds so it renders through the same latency formatter as the
// rest of the page; the timestamp is rendered compactly in UTC.
func toExemplarRows(exemplars []storage.ExemplarRow) []exemplarRow {
	out := make([]exemplarRow, 0, len(exemplars))
	for _, e := range exemplars {
		out = append(out, exemplarRow{
			Time:       e.At.UTC().Format(exemplarTimeLayout),
			StatusCode: e.StatusCode,
			Latency:    float64(e.DurationNs) / 1e9,
			FullTrace:  e.TraceID,
			ShortTrace: truncID(e.TraceID),
			FullReq:    e.RequestID,
			ShortReq:   truncID(e.RequestID),
		})
	}
	return out
}

// classLatencyRow is one rendered row of the success-vs-error latency split: a
// status class with its per-class request count and p50/p95/p99. Only classes
// with traffic in the window are emitted (zero-traffic classes are omitted).
type classLatencyRow struct {
	Class string
	Count uint64
	P50   float64
	P95   float64
	P99   float64
}

// statusClassLabels maps the storage Enum8 status_class keys to the short
// dashboard labels, in fixed display order. It is the single source of order for
// the latency-split panel, matching the class breakdown's 2xx…no_status voice.
var statusClassLabels = []struct{ Key, Label string }{
	{"STATUS_CLASS_2XX", "2xx"},
	{"STATUS_CLASS_3XX", "3xx"},
	{"STATUS_CLASS_4XX", "4xx"},
	{"STATUS_CLASS_5XX", "5xx"},
	{"STATUS_CLASS_NO_STATUS", "no_status"},
}

// toClassLatencyRows maps the per-status-class latency split into display rows in
// the fixed class order, dropping zero-traffic classes so the compact table only
// shows classes that actually saw requests in the window.
func toClassLatencyRows(byClass map[string]storage.ClassLatency) []classLatencyRow {
	out := make([]classLatencyRow, 0, len(statusClassLabels))
	for _, c := range statusClassLabels {
		cl := byClass[c.Key]
		if cl.Count == 0 {
			continue
		}
		out = append(out, classLatencyRow{
			Class: c.Label,
			Count: cl.Count,
			P50:   cl.P50,
			P95:   cl.P95,
			P99:   cl.P99,
		})
	}
	return out
}

// errorClassRow is one rendered row of the endpoint-detail error-class panel: a
// normalized error label and how many requests carried it. Rows arrive ordered by
// count descending from storage, so no re-sort is needed. It answers "5xx up
// because of what?".
type errorClassRow struct {
	Class string
	Count uint64
}

// toErrorClassRows maps the storage error-class stats into display rows,
// preserving the storage order (by count descending).
func toErrorClassRows(stats []storage.ErrorClassStat) []errorClassRow {
	out := make([]errorClassRow, 0, len(stats))
	for _, s := range stats {
		out = append(out, errorClassRow{Class: s.Class, Count: s.Count})
	}
	return out
}

// noStatusReasonLabels maps the proto NoStatusReason enum value (as stored UInt8)
// to a short dashboard label. An unknown value falls through to "other" so a
// stray key never renders blank.
var noStatusReasonLabels = map[uint8]string{
	0: "unspecified",
	1: "context canceled",
	2: "context deadline",
	3: "write error",
	4: "panic",
	5: "other",
}

// noStatusReasonRow is one rendered row of the NO_STATUS reason panel: a
// human-readable reason and how many aborted requests it explains. It answers
// whether NO_STATUS is "all deadline-exceeded" vs canceling vs crashing.
type noStatusReasonRow struct {
	Reason string
	Count  uint64
}

// toNoStatusReasonRows maps the storage NO_STATUS reason stats into display rows,
// resolving each enum value to its label and preserving the storage order (by
// reason). An unknown reason value is labelled "other".
func toNoStatusReasonRows(stats []storage.NoStatusReasonStat) []noStatusReasonRow {
	out := make([]noStatusReasonRow, 0, len(stats))
	for _, s := range stats {
		label, ok := noStatusReasonLabels[s.Reason]
		if !ok {
			label = "other"
		}
		out = append(out, noStatusReasonRow{Reason: label, Count: s.Count})
	}
	return out
}

// downstreamView drives the self-vs-downstream time panel: the average time an
// endpoint spends on its own work versus waiting on downstream calls, as a
// stacked bar. HasData is false when no downstream timing was reported (the
// RoundTripper is unwired or no request made a downstream call), so the template
// shows a wire-it-up empty state instead of a misleading all-self bar.
type downstreamView struct {
	HasData      bool
	SelfSeconds  float64 // average self time per request, in seconds (for msf)
	DownSeconds  float64 // average downstream time per request, in seconds
	SelfWidth    string  // CSS width for the self segment, e.g. "63.0%"
	DownWidth    string  // CSS width for the downstream segment, e.g. "37.0%"
	DownFraction float64 // downstream share of total time, 0..1 (for pctd)
}

// toDownstreamView computes the self-vs-downstream split from the aggregate
// stat. Downstream time is clamped to the total request time (a merge or clock
// skew could momentarily push it over), so self time is never negative and the
// bar widths always sum to 100%. Averages are per request, converted to seconds
// so they format through the same msf helper as every other latency on the page.
func toDownstreamView(s storage.DownstreamStat) downstreamView {
	if s.Count == 0 || s.SumDownstreamNs == 0 {
		return downstreamView{}
	}
	total := s.SumDurationNs
	down := s.SumDownstreamNs
	if down > total {
		down = total
	}
	self := total - down
	frac := 0.0
	if total > 0 {
		frac = float64(down) / float64(total)
	}
	downPct := frac * 100
	return downstreamView{
		HasData:      true,
		SelfSeconds:  float64(self) / float64(s.Count) / 1e9,
		DownSeconds:  float64(down) / float64(s.Count) / 1e9,
		SelfWidth:    strconv.FormatFloat(100-downPct, 'f', 1, 64) + "%",
		DownWidth:    strconv.FormatFloat(downPct, 'f', 1, 64) + "%",
		DownFraction: frac,
	}
}

// resourceRow is one rendered row of the per-instance USE (saturation) panel: a
// replica's per-window CPU intensity and GC-pause share plus its peak memory and
// goroutine gauges. Rows arrive ordered by instance from storage. It answers "did
// p99 rise because this replica's GC ate wall time or goroutines blew up?".
type resourceRow struct {
	Instance      string
	CoresUsed     float64 // average cores consumed over the window (cpu_ns / window_ns)
	GCShare       float64 // fraction of wall time in STW GC pause, 0..1 (for pctd)
	RSSBytes      float64 // peak resident-memory proxy, in bytes (for the bytes fmt)
	HeapBytes     float64 // peak live-heap bytes
	Goroutines    uint64  // peak goroutine count
	GCFreq        float64 // GC cycles per second over the window (num_gc / winSeconds)
	GCCPUFraction float64 // fraction of CPU time in GC, 0..1 (for pctd)
	AllocRate     float64 // heap bytes allocated per second (total_alloc / winSeconds)
	AvgAllocSize  float64 // average allocation size in bytes (total_alloc / mallocs)
}

// toResourceRows maps the storage per-instance USE stats into display rows,
// converting the summed per-window CPU and GC-pause deltas into intensities: cores
// consumed (cpu_ns / window_ns) and STW GC-pause share of wall time. The MemStats
// deltas become rates over the window (GC frequency, allocation rate), and the
// per-object average allocation size is total_alloc / mallocs (guarded against a
// zero malloc count). A non-positive window yields zero intensities/rates, and GC
// share is clamped to [0,1]. Byte gauges become float64 for the bytes formatter.
// Storage order (by instance) is preserved.
func toResourceRows(stats []storage.InstanceResourceStat, winSeconds float64) []resourceRow {
	out := make([]resourceRow, 0, len(stats))
	for _, s := range stats {
		var cores, gcShare, gcFreq, allocRate float64
		if winSeconds > 0 {
			winNs := winSeconds * 1e9
			cores = float64(s.CPUNs) / winNs
			gcShare = float64(s.GCPauseNs) / winNs
			if gcShare > 1 {
				gcShare = 1
			}
			gcFreq = float64(s.NumGC) / winSeconds
			allocRate = float64(s.TotalAllocBytes) / winSeconds
		}
		var avgAlloc float64
		if s.Mallocs > 0 {
			avgAlloc = float64(s.TotalAllocBytes) / float64(s.Mallocs)
		}
		out = append(out, resourceRow{
			Instance:      s.Instance,
			CoresUsed:     cores,
			GCShare:       gcShare,
			RSSBytes:      float64(s.RSSBytes),
			HeapBytes:     float64(s.HeapAllocBytes),
			Goroutines:    s.Goroutines,
			GCFreq:        gcFreq,
			GCCPUFraction: s.GCCPUFraction,
			AllocRate:     allocRate,
			AvgAllocSize:  avgAlloc,
		})
	}
	return out
}
