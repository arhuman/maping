package web

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/arhuman/maping/server/internal/storage"
)

// serveEndpointDetail renders level 3: the detail page (time-series chart via
// /api/series, latency histogram via /api/histogram, and the class breakdown
// beside the headline error rate).
func (h *Handler) serveEndpointDetail(w http.ResponseWriter, r *http.Request) {
	tid, ok := h.resolveTenant(w, r)
	if !ok {
		return
	}
	service := r.PathValue("service")
	method := r.URL.Query().Get("method")
	route := r.URL.Query().Get("route")
	winKey := normalizeWindow(r.URL.Query().Get("win"))
	dur := windowDur[winKey]
	from, to, custom := resolveDetailRange(r, dur)
	step := seriesStep
	if custom {
		step = adaptiveStep(to.Sub(from))
	}

	tq := h.q.Tenant(tid)
	detail, err := tq.EndpointDetail(r.Context(), service, method, route, from, to)
	if err != nil {
		h.serverError(w, "endpoint detail query", err)
		return
	}

	// The chart and the debuggability panels are all secondary to the detail
	// headline: a query failure logs and renders an empty panel (its zero value)
	// rather than 500-ing the page. softQuery centralizes that fail-soft policy.
	ctx := r.Context()
	points := softQuery(ctx, h, "series", func(ctx context.Context) ([]storage.TimePoint, error) {
		return tq.SeriesOverTime(ctx, service, method, route, from, to, step)
	})
	instances := softQuery(ctx, h, "instances", func(ctx context.Context) ([]storage.InstanceStat, error) {
		return tq.InstancesForEndpoint(ctx, service, method, route, from, to)
	})
	versions := softQuery(ctx, h, "versions", func(ctx context.Context) ([]storage.VersionStat, error) {
		return tq.VersionsForEndpoint(ctx, service, method, route, from, to)
	})
	exemplars := softQuery(ctx, h, "exemplars", func(ctx context.Context) ([]storage.ExemplarRow, error) {
		return tq.ExemplarsForEndpoint(ctx, service, method, route, from, to)
	})
	byClass := softQuery(ctx, h, "latency-by-class", func(ctx context.Context) (map[string]storage.ClassLatency, error) {
		return tq.LatencyByStatusClass(ctx, service, method, route, from, to)
	})
	errorClasses := softQuery(ctx, h, "error-classes", func(ctx context.Context) ([]storage.ErrorClassStat, error) {
		return tq.ErrorClassesForEndpoint(ctx, service, method, route, from, to)
	})
	noStatusReasons := softQuery(ctx, h, "no-status-reasons", func(ctx context.Context) ([]storage.NoStatusReasonStat, error) {
		return tq.NoStatusReasonsForEndpoint(ctx, service, method, route, from, to)
	})
	downstream := softQuery(ctx, h, "downstream", func(ctx context.Context) (storage.DownstreamStat, error) {
		return tq.DownstreamForEndpoint(ctx, service, method, route, from, to)
	})
	resources := softQuery(ctx, h, "instance-resources", func(ctx context.Context) ([]storage.InstanceResourceStat, error) {
		return tq.InstanceResourcesForService(ctx, service, from, to)
	})

	dv := toDetailView(detail)
	crumbs := []crumb{{Label: "services", Href: withWin("/", winKey)}, {Label: service, Href: withWin("/services/"+service, winKey)}, {Label: method + " " + route}}
	h.render(w, "detail", detailData{
		Shell:           h.buildShell(r, "overview", crumbs, method+" "+route, true, winKey),
		Service:         service,
		Method:          method,
		Route:           route,
		Detail:          dv,
		Stats:           detailStats(dv, to.Sub(from).Seconds()),
		StatusBars:      statusBarsFor(dv),
		Debug:           buildDebugContext(service, method, route, from, to, dv, dominantVersion(versions)),
		Range:           buildDetailRange(service, method, route, winKey, from, to, time.Now().UTC(), custom),
		TSChart:         timeSeriesSVG(points, step),
		HistChart:       histogramSVG(detail.Histogram, detail.P50, detail.P95, detail.P99),
		Instances:       toInstanceRows(instances),
		Versions:        toVersionRows(versions),
		Exemplars:       toExemplarRows(exemplars),
		ClassLatency:    toClassLatencyRows(byClass),
		ErrorClasses:    toErrorClassRows(errorClasses),
		NoStatusReasons: toNoStatusReasonRows(noStatusReasons),
		Downstream:      toDownstreamView(downstream),
		Resources:       toResourceRows(resources),
	})
}

// softQuery runs a secondary detail-panel query fail-soft: on error it logs under
// "web: detail <label>" and returns the zero value of T (nil slice/map, empty
// struct) so the panel renders empty instead of failing the whole page. ctx is
// threaded into run so the request context propagates to the query. It is a free
// function, not a method, because Go methods cannot take type parameters.
func softQuery[T any](ctx context.Context, h *Handler, label string, run func(context.Context) (T, error)) T {
	v, err := run(ctx)
	if err != nil {
		h.log.Error("web: detail "+label, slog.Any("err", err))
		var zero T
		return zero
	}
	return v
}
