package web

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// serveSeries is a legacy time-series JSON endpoint. The live
// detail chart is inline SVG (timeSeriesSVG); this endpoint is retained but unused by
// the current UI.
func (h *Handler) serveSeries(w http.ResponseWriter, r *http.Request) {
	tid, ok := h.resolveTenant(w, r)
	if !ok {
		return
	}
	service := r.URL.Query().Get("service")
	method := r.URL.Query().Get("method")
	route := r.URL.Query().Get("route")
	from, to := windowRange(window)

	points, err := h.q.Tenant(tid).SeriesOverTime(r.Context(), service, method, route, from, to, seriesStep)
	if err != nil {
		h.serverError(w, "series query", err)
		return
	}

	out := make([]seriesPoint, 0, len(points))
	for _, p := range points {
		out = append(out, seriesPoint{
			TS:        p.TS.Unix(),
			Count:     p.Count,
			ErrorRate: p.ErrorRate,
			P50:       p.P50,
			P95:       p.P95,
			P99:       p.P99,
		})
	}
	writeJSON(w, h.log, out)
}

// serveHistogram is the latency-histogram JSON endpoint feeding the detail
// bar chart from the merged DDSketch buckets.
func (h *Handler) serveHistogram(w http.ResponseWriter, r *http.Request) {
	tid, ok := h.resolveTenant(w, r)
	if !ok {
		return
	}
	service := r.URL.Query().Get("service")
	method := r.URL.Query().Get("method")
	route := r.URL.Query().Get("route")
	from, to := windowRange(window)

	detail, err := h.q.Tenant(tid).EndpointDetail(r.Context(), service, method, route, from, to)
	if err != nil {
		h.serverError(w, "histogram query", err)
		return
	}

	out := make([]histogramBar, 0, len(detail.Histogram))
	for _, b := range detail.Histogram {
		out = append(out, histogramBar{Latency: b.LatencySeconds, Count: b.Count})
	}
	writeJSON(w, h.log, out)
}

// writeJSON encodes v as JSON, logging on encode error.
func writeJSON(w http.ResponseWriter, log *slog.Logger, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Error("web: encode json", slog.Any("err", err))
	}
}

// seriesPoint is the JSON shape returned by the legacy /api/series endpoint.
type seriesPoint struct {
	TS        int64   `json:"ts"` // unix seconds
	Count     uint64  `json:"count"`
	ErrorRate float64 `json:"errorRate"`
	P50       float64 `json:"p50"`
	P95       float64 `json:"p95"`
	P99       float64 `json:"p99"`
}

// histogramBar is the JSON shape returned by the legacy /api/histogram endpoint.
type histogramBar struct {
	Latency float64 `json:"latency"` // bucket latency in seconds
	Count   uint64  `json:"count"`
}
