package web

import (
	"fmt"
	"html/template"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/arhuman/maping/server/internal/storage"
)

// This file renders the detail-page charts as inline SVG on the server, so the
// dashboard needs no client JS or chart library (CSP-friendly, unit-testable).
// The returned template.HTML is built here from trusted numeric data — user
// strings never reach it — so it is safe to emit unescaped. Colors are concrete
// hex, not CSS var(), since SVG presentation attributes do not resolve var().

// timeSeriesSVG draws the synchronized traffic/errors/latency timeline: rate
// (accent, left axis), errors/sec (red, sharing the left axis since errors are a
// subset of requests) as a filled band, and p95 (violet, right axis in ms). One
// chart answers "is it up, is it erroring, is it slow" at a glance. Fewer than two
// points cannot form a line, so it renders an empty frame instead.
func timeSeriesSVG(points []storage.TimePoint, step time.Duration) template.HTML {
	const (
		w, hgt         = 560.0, 250.0
		pL, pR, pT, pB = 44.0, 46.0, 14.0, 26.0
	)
	if len(points) < 2 {
		return emptyChart(w, hgt, "no time-series data yet")
	}
	stepSec := step.Seconds()
	if stepSec <= 0 {
		stepSec = 1
	}
	n := len(points)
	rate := make([]float64, n)
	errPS := make([]float64, n)
	p95 := make([]float64, n)
	var rMax, pMax float64
	for i, p := range points {
		rate[i] = float64(p.Count) / stepSec
		// errors/sec share the rate (left) axis: errors are a subset of requests,
		// so errPS <= rate <= rMax and no separate scale is needed.
		errPS[i] = float64(p.Count) * p.ErrorRate / stepSec
		p95[i] = p.P95
		if rate[i] > rMax {
			rMax = rate[i]
		}
		if p95[i] > pMax {
			pMax = p95[i]
		}
	}
	rMax *= 1.15
	pMax *= 1.2
	if rMax <= 0 {
		rMax = 1
	}
	if pMax <= 0 {
		pMax = 1
	}
	x := func(i int) float64 { return pL + float64(i)*(w-pL-pR)/float64(n-1) }
	yr := func(v float64) float64 { return hgt - pB - (v/rMax)*(hgt-pT-pB) }
	yp := func(v float64) float64 { return hgt - pB - (v/pMax)*(hgt-pT-pB) }

	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %g %g" width="100%%" style="display:block;height:auto">`, w, hgt)
	b.WriteString(`<defs><linearGradient id="ts" x1="0" y1="0" x2="0" y2="1"><stop offset="0%" stop-color="#B4F14A" stop-opacity="0.22"></stop><stop offset="100%" stop-color="#B4F14A" stop-opacity="0"></stop></linearGradient><linearGradient id="tse" x1="0" y1="0" x2="0" y2="1"><stop offset="0%" stop-color="#FF6B6B" stop-opacity="0.30"></stop><stop offset="100%" stop-color="#FF6B6B" stop-opacity="0"></stop></linearGradient></defs>`)
	for g := 0; g <= 3; g++ {
		y := pT + float64(g)*(hgt-pT-pB)/3
		fmt.Fprintf(&b, `<line x1="%g" y1="%.1f" x2="%.1f" y2="%.1f" stroke="rgba(255,255,255,.055)" stroke-width="1"></line>`, pL, y, w-pR, y)
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" text-anchor="end" fill="#69727F" style="font:500 9px var(--mono)">%.0f</text>`, pL-8, y+3, rMax*(1-float64(g)/3))
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" text-anchor="start" fill="#A78BFA" style="font:500 9px var(--mono)">%.0f</text>`, w-pR+8, y+3, pMax*(1-float64(g)/3)*1000)
	}
	rLine := svgPath(x, yr, rate)
	eLine := svgPath(x, yr, errPS)
	fmt.Fprintf(&b, `<path d="%s L%.1f,%.1f L%.1f,%.1f Z" fill="url(#ts)"></path>`, rLine, x(n-1), hgt-pB, pL, hgt-pB)
	fmt.Fprintf(&b, `<path d="%s L%.1f,%.1f L%.1f,%.1f Z" fill="url(#tse)"></path>`, eLine, x(n-1), hgt-pB, pL, hgt-pB)
	fmt.Fprintf(&b, `<path d="%s" fill="none" stroke="#B4F14A" stroke-width="2" stroke-linejoin="round"></path>`, rLine)
	fmt.Fprintf(&b, `<path d="%s" fill="none" stroke="#FF6B6B" stroke-width="2" stroke-linejoin="round"></path>`, eLine)
	fmt.Fprintf(&b, `<path d="%s" fill="none" stroke="#A78BFA" stroke-width="2" stroke-linejoin="round"></path>`, svgPath(x, yp, p95))
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// histogramSVG draws the merged DDSketch buckets as log-spaced bars with dashed
// p50/p95/p99 markers, matching the design's latency distribution. Empty input
// renders an empty frame.
func histogramSVG(bars []storage.HistogramBar, p50, p95, p99 float64) template.HTML {
	const (
		w, hgt         = 760.0, 220.0
		pL, pR, pT, pB = 8.0, 8.0, 12.0, 34.0
	)
	if len(bars) == 0 {
		return emptyChart(w, hgt, "no latency data yet")
	}
	sorted := append([]storage.HistogramBar(nil), bars...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].LatencySeconds < sorted[j].LatencySeconds })
	var cMax uint64
	for _, bar := range sorted {
		if bar.Count > cMax {
			cMax = bar.Count
		}
	}
	if cMax == 0 {
		cMax = 1
	}
	bw := (w - pL - pR) / float64(len(sorted))

	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %g %g" width="100%%" style="display:block;height:auto">`, w, hgt)
	for i, bar := range sorted {
		bh := float64(bar.Count) / float64(cMax) * (hgt - pT - pB)
		x := pL + float64(i)*bw + 3
		y := hgt - pB - bh
		fmt.Fprintf(&b, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" rx="3" fill="#A78BFA" opacity="0.78"></rect>`, x, y, bw-6, bh)
		if i%2 == 0 {
			fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" text-anchor="middle" fill="#69727F" style="font:500 9px var(--mono)">%s</text>`, pL+float64(i)*bw+bw/2, hgt-pB+16, latencyLabel(bar.LatencySeconds))
		}
	}
	marker := func(val float64, label, color string) {
		idx := 0
		for i, bar := range sorted {
			if bar.LatencySeconds <= val {
				idx = i
			}
		}
		x := pL + float64(idx)*bw + bw/2
		fmt.Fprintf(&b, `<line x1="%.1f" y1="%g" x2="%.1f" y2="%g" stroke="%s" stroke-width="1.4" stroke-dasharray="3 3" opacity="0.9"></line>`, x, pT, x, hgt-pB, color)
		fmt.Fprintf(&b, `<text x="%.1f" y="%g" text-anchor="middle" fill="%s" style="font:700 9px var(--mono)">%s</text>`, x, pT+2, color, label)
	}
	marker(p50, "p50", "#5FE3A1")
	marker(p95, "p95", "#B4F14A")
	marker(p99, "p99", "#FF6B6B")
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// memoryTrendSVG draws the per-window fleet memory trend: peak post-GC live heap
// (accent, the leak-vs-burst signal) as the primary line and peak in-use heap
// (muted, secondary) over the same window as the timeline, on a shared byte y-axis.
// A climbing-and-held post-GC line is the visual leak; a spike that returns is a
// burst. Fewer than two points cannot form a line, so it renders an empty frame.
// The x-axis is index-based (one slot per bucket), so the step is not needed; the
// param keeps the signature parallel to timeSeriesSVG and marks the shared window.
func memoryTrendSVG(points []storage.MemoryTrendPoint, _ time.Duration) template.HTML {
	const (
		w, hgt         = 560.0, 210.0
		pL, pR, pT, pB = 62.0, 12.0, 14.0, 20.0
	)
	if len(points) < 2 {
		return emptyChart(w, hgt, "no memory data yet")
	}
	n := len(points)
	postGC := make([]float64, n)
	inuse := make([]float64, n)
	var vMax float64
	for i, p := range points {
		postGC[i] = float64(p.PostGCHeapBytes)
		inuse[i] = float64(p.HeapInuseBytes)
		if postGC[i] > vMax {
			vMax = postGC[i]
		}
		if inuse[i] > vMax {
			vMax = inuse[i]
		}
	}
	vMax *= 1.15
	if vMax <= 0 {
		vMax = 1
	}
	x := func(i int) float64 { return pL + float64(i)*(w-pL-pR)/float64(n-1) }
	y := func(v float64) float64 { return hgt - pB - (v/vMax)*(hgt-pT-pB) }

	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %g %g" width="100%%" style="display:block;height:auto">`, w, hgt)
	b.WriteString(`<defs><linearGradient id="mt" x1="0" y1="0" x2="0" y2="1"><stop offset="0%" stop-color="#B4F14A" stop-opacity="0.20"></stop><stop offset="100%" stop-color="#B4F14A" stop-opacity="0"></stop></linearGradient></defs>`)
	for g := 0; g <= 3; g++ {
		gy := pT + float64(g)*(hgt-pT-pB)/3
		fmt.Fprintf(&b, `<line x1="%g" y1="%.1f" x2="%.1f" y2="%.1f" stroke="rgba(255,255,255,.055)" stroke-width="1"></line>`, pL, gy, w-pR, gy)
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" text-anchor="end" fill="#69727F" style="font:500 9px var(--mono)">%s</text>`, pL-8, gy+3, bytesLabel(vMax*(1-float64(g)/3)))
	}
	pgLine := svgPath(x, y, postGC)
	fmt.Fprintf(&b, `<path d="%s L%.1f,%.1f L%.1f,%.1f Z" fill="url(#mt)"></path>`, pgLine, x(n-1), hgt-pB, pL, hgt-pB)
	fmt.Fprintf(&b, `<path d="%s" fill="none" stroke="#69727F" stroke-width="1.6" stroke-dasharray="4 3" stroke-linejoin="round"></path>`, svgPath(x, y, inuse))
	fmt.Fprintf(&b, `<path d="%s" fill="none" stroke="#B4F14A" stroke-width="2" stroke-linejoin="round"></path>`, pgLine)
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// bytesLabel renders a byte value as a compact axis label, reusing the shared
// 1024-based byte formatter ("240 MB").
func bytesLabel(v float64) string {
	if v < 0 {
		v = 0
	}
	return fmtBytes(v)
}

// svgPath builds an "M/L x,y" polyline path from the mapped points.
func svgPath(x func(int) float64, y func(float64) float64, vals []float64) string {
	var b strings.Builder
	for i, v := range vals {
		cmd := "L"
		if i == 0 {
			cmd = "M"
		}
		fmt.Fprintf(&b, "%s%.1f,%.1f ", cmd, x(i), y(v))
	}
	return strings.TrimSpace(b.String())
}

// latencyLabel renders a bucket's seconds as a compact axis label (ms below 1s).
func latencyLabel(sec float64) string {
	ms := sec * 1000
	if ms >= 1000 {
		return strings.TrimSuffix(strconv.FormatFloat(ms/1000, 'f', 1, 64), ".0") + "s"
	}
	return strconv.FormatFloat(ms, 'f', 0, 64) + "ms"
}

// emptyChart renders a muted placeholder frame so a data-less detail page still
// lays out cleanly instead of collapsing.
func emptyChart(w, hgt float64, msg string) template.HTML {
	return template.HTML(fmt.Sprintf(
		`<svg viewBox="0 0 %g %g" width="100%%" style="display:block;height:auto"><text x="%g" y="%g" text-anchor="middle" fill="#69727F" style="font:500 12px var(--mono)">%s</text></svg>`,
		w, hgt, w/2, hgt/2, template.HTMLEscapeString(msg)))
}
