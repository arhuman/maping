package web

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
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
	// rather than 500-ing the page. loadDetailPanels runs them concurrently, so the
	// page waits on the slowest one rather than the sum of all nine round-trips.
	p := h.loadDetailPanels(r.Context(), tq, service, method, route, from, to, step)

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
		Debug:           buildDebugContext(service, method, route, from, to, dv, dominantVersion(p.versions)),
		Range:           buildDetailRange(service, method, route, winKey, from, to, time.Now().UTC(), custom),
		TSChart:         timeSeriesSVG(p.points, step),
		HistChart:       histogramSVG(detail.Histogram, detail.P50, detail.P95, detail.P99),
		Instances:       toInstanceRows(p.instances),
		Versions:        toVersionRows(p.versions),
		Exemplars:       toExemplarRows(p.exemplars),
		ClassLatency:    toClassLatencyRows(p.byClass),
		ErrorClasses:    toErrorClassRows(p.errorClasses),
		NoStatusReasons: toNoStatusReasonRows(p.noStatusReasons),
		Downstream:      toDownstreamView(p.downstream),
		Resources:       toResourceRows(p.resources),
	})
}

// detailPanels holds the nine secondary detail-page query results, loaded
// concurrently by loadDetailPanels.
type detailPanels struct {
	points          []storage.TimePoint
	instances       []storage.InstanceStat
	versions        []storage.VersionStat
	exemplars       []storage.ExemplarRow
	byClass         map[string]storage.ClassLatency
	errorClasses    []storage.ErrorClassStat
	noStatusReasons []storage.NoStatusReasonStat
	downstream      storage.DownstreamStat
	resources       []storage.InstanceResourceStat
}

// loadDetailPanels runs the nine independent secondary detail queries
// concurrently and returns once all have completed (each fail-soft to its zero
// value via softQuery). Every goroutine writes only its own field, so the
// concurrent writes never overlap; the caller sees the sum of nine queries as
// the latency of the slowest one.
func (h *Handler) loadDetailPanels(ctx context.Context, tq ScopedQuery, service, method, route string, from, to time.Time, step time.Duration) detailPanels {
	var p detailPanels
	var wg sync.WaitGroup
	run := func(fn func()) {
		wg.Add(1)
		go func() { defer wg.Done(); fn() }()
	}
	run(func() {
		p.points = softQuery(ctx, h, "series", func(ctx context.Context) ([]storage.TimePoint, error) {
			return tq.SeriesOverTime(ctx, service, method, route, from, to, step)
		})
	})
	run(func() {
		p.instances = softQuery(ctx, h, "instances", func(ctx context.Context) ([]storage.InstanceStat, error) {
			return tq.InstancesForEndpoint(ctx, service, method, route, from, to)
		})
	})
	run(func() {
		p.versions = softQuery(ctx, h, "versions", func(ctx context.Context) ([]storage.VersionStat, error) {
			return tq.VersionsForEndpoint(ctx, service, method, route, from, to)
		})
	})
	run(func() {
		p.exemplars = softQuery(ctx, h, "exemplars", func(ctx context.Context) ([]storage.ExemplarRow, error) {
			return tq.ExemplarsForEndpoint(ctx, service, method, route, from, to)
		})
	})
	run(func() {
		p.byClass = softQuery(ctx, h, "latency-by-class", func(ctx context.Context) (map[string]storage.ClassLatency, error) {
			return tq.LatencyByStatusClass(ctx, service, method, route, from, to)
		})
	})
	run(func() {
		p.errorClasses = softQuery(ctx, h, "error-classes", func(ctx context.Context) ([]storage.ErrorClassStat, error) {
			return tq.ErrorClassesForEndpoint(ctx, service, method, route, from, to)
		})
	})
	run(func() {
		p.noStatusReasons = softQuery(ctx, h, "no-status-reasons", func(ctx context.Context) ([]storage.NoStatusReasonStat, error) {
			return tq.NoStatusReasonsForEndpoint(ctx, service, method, route, from, to)
		})
	})
	run(func() {
		p.downstream = softQuery(ctx, h, "downstream", func(ctx context.Context) (storage.DownstreamStat, error) {
			return tq.DownstreamForEndpoint(ctx, service, method, route, from, to)
		})
	})
	run(func() {
		p.resources = softQuery(ctx, h, "instance-resources", func(ctx context.Context) ([]storage.InstanceResourceStat, error) {
			return tq.InstanceResourcesForService(ctx, service, from, to)
		})
	})
	wg.Wait()
	return p
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
