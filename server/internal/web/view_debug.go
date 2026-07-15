package web

import (
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// debugContext is the shareable triage block on the endpoint-detail page: the
// exact coordinates of what is being looked at (service, method, route), the
// precise window bounds, the dominant error class, and the tail latencies.
// Summary is the one-line copyable form; the page URL is the shareable link.
type debugContext struct {
	Service     string
	Method      string
	Route       string
	From        string // exact window start (UTC)
	To          string // exact window end (UTC)
	DominantErr string // highest-count error class, or "none"
	Version     string // dominant deploy_version, or "" when unknown
	P95         string
	P99         string
	Summary     string // one-line, copy-to-clipboard
}

// debugTimeLayout formats the window bounds to second precision in UTC so a
// pasted debug context is unambiguous across timezones.
const debugTimeLayout = "2006-01-02 15:04:05 UTC"

// detailRange is the resolved detail-page time range plus the plain-link controls
// that re-scope it. Every control is an <a> href (ADR-0008 JS budget: navigation
// is links, the server re-renders), preserving method/route/win. PanRightHref is
// empty when the range is already flush with now — you cannot pan into the future.
type detailRange struct {
	Custom       bool   // true when ?from=&to= overrode the preset window
	Label        string // the preset text, or the exact UTC bounds when custom
	ResetHref    string // back to the preset window (drops from/to); "" unless Custom
	ZoomOutHref  string // 2x the current span, centred and capped at now
	PanLeftHref  string // shift the window back by half its span
	PanRightHref string // shift forward by half a span (clamped to now); "" at now
}

// detailURL builds an endpoint-detail link with the query params escaped. from/to
// are attached only when both are non-nil (a custom range); otherwise the link
// carries just method/route/win, which resets to the preset window.
func detailURL(service, method, route, winKey string, from, to *time.Time) string {
	q := url.Values{}
	q.Set("method", method)
	q.Set("route", route)
	if winKey != "" {
		q.Set("win", winKey)
	}
	if from != nil && to != nil {
		q.Set("from", strconv.FormatInt(from.Unix(), 10))
		q.Set("to", strconv.FormatInt(to.Unix(), 10))
	}
	return "/services/" + service + "/endpoint?" + q.Encode()
}

// detailRangeLayout is the compact UTC form for the custom-range pill; "MST" on a
// UTC time renders as "UTC", so the label is unambiguous.
const detailRangeLayout = "Jan 2 15:04"

// buildDetailRange assembles the range pill and the zoom-out/pan/reset controls
// for the detail chart. custom marks whether ?from=&to= is active; from/to are the
// resolved bounds (UTC) and now is the clamp for panning into the future. Zoom-out
// and pan are always offered — from a preset they are the entry into a custom
// range — while reset appears only once a custom range is active.
func buildDetailRange(service, method, route, winKey string, from, to, now time.Time, custom bool) detailRange {
	dr := detailRange{Custom: custom}
	if custom {
		dr.Label = from.Format(detailRangeLayout) + " – " + to.Format(detailRangeLayout+" MST")
		dr.ResetHref = detailURL(service, method, route, winKey, nil, nil)
	} else {
		dr.Label = windowText[winKey]
	}

	span := to.Sub(from)
	half := span / 2

	zf, zt := from.Add(-half), to.Add(half)
	if zt.After(now) {
		zt = now
	}
	dr.ZoomOutHref = detailURL(service, method, route, winKey, &zf, &zt)

	plf, plt := from.Add(-half), to.Add(-half)
	dr.PanLeftHref = detailURL(service, method, route, winKey, &plf, &plt)

	// Pan right only when there is future headroom; clamp the trailing edge to now
	// so a forward pan never queries beyond the present.
	if to.Before(now) {
		shift := half
		if gap := now.Sub(to); gap < shift {
			shift = gap
		}
		prf, prt := from.Add(shift), to.Add(shift)
		dr.PanRightHref = detailURL(service, method, route, winKey, &prf, &prt)
	}
	return dr
}

// buildDebugContext assembles the detail-page debug block from the request
// coordinates, the exact window bounds, the detail view, and the dominant
// deploy_version (empty when there is no usable version data, in which case the
// release segment is omitted from the summary rather than rendered blank).
func buildDebugContext(service, method, route string, from, to time.Time, dv detailView, version string) debugContext {
	fromS := from.UTC().Format(debugTimeLayout)
	toS := to.UTC().Format(debugTimeLayout)
	dom := dominantErrorClass(dv.Classes)
	p95, p99 := fmtMsFull(dv.P95), fmtMsFull(dv.P99)
	versionSeg := ""
	if version != "" {
		versionSeg = " · version " + version
	}
	return debugContext{
		Service:     service,
		Method:      method,
		Route:       route,
		From:        fromS,
		To:          toS,
		DominantErr: dom,
		Version:     version,
		P95:         p95,
		P99:         p99,
		Summary: fmt.Sprintf("%s · %s %s · %s → %s · dominant error %s%s · p95 %s · p99 %s",
			service, method, route, fromS, toS, dom, versionSeg, p95, p99),
	}
}

// dominantErrorClass returns the highest-count error class (4xx/5xx/no_status),
// or "none" when the endpoint has no errors in the window.
func dominantErrorClass(classes []statusClassView) string {
	best := ""
	var bestCount uint64
	for _, c := range classes {
		if c.IsError && c.Count > bestCount {
			best, bestCount = c.Class, c.Count
		}
	}
	if best == "" {
		return "none"
	}
	return best
}
