package web

import (
	"strconv"
	"strings"
)

// This file holds the pure presentation formatters and colour-class helpers the
// dashboard chrome needs: value formatting (rates, counts, latency, bytes) and
// CSS class selection. Everything here is I/O-free, so it is unit-testable
// without HTTP or ClickHouse, and colours are emitted as CSS class names (never
// dynamic inline colours) so html/template never has to filter a var() out of a
// style attribute.

// ---------------------------------------------------------------- formatters

// fmtRate renders a per-second rate: 2103 -> "2.1k", 903 -> "903", 0.285 -> "0.29".
// Sub-1 rates keep two significant figures so a low-throughput or bursty endpoint
// (whose count/window average is fractional) never renders as "0/s" next to a
// nonzero request count — only a truly zero rate shows "0".
func fmtRate(r float64) string {
	switch {
	case r >= 1000:
		return strings.TrimSuffix(strconv.FormatFloat(r/1000, 'f', 1, 64), ".0") + "k"
	case r >= 1:
		return strconv.FormatFloat(r, 'f', 0, 64)
	case r > 0:
		return strconv.FormatFloat(r, 'g', 2, 64)
	default:
		return "0"
	}
}

// fmtCount renders a request total: 4.2M / 12.0k / 830.
func fmtCount(c uint64) string {
	f := float64(c)
	switch {
	case f >= 1e6:
		return strconv.FormatFloat(f/1e6, 'f', 1, 64) + "M"
	case f >= 1e3:
		return strconv.FormatFloat(f/1e3, 'f', 1, 64) + "k"
	default:
		return strconv.FormatUint(c, 10)
	}
}

// fmtPctD renders a [0,1] fraction as a percentage with 1 decimal at/above 10%
// and 2 below, matching the design (0.021 -> "2.10%", 0.163 -> "16.3%").
func fmtPctD(f float64) string {
	dec := 2
	if f >= 0.1 {
		dec = 1
	}
	return strconv.FormatFloat(f*100, 'f', dec, 64) + "%"
}

// fmtMsVal / fmtMsUnit split a seconds value into the design's number + unit:
// >=1s -> "1.18" / "s"; <10ms -> "2.0" / "ms"; else "88" / "ms".
func fmtMsVal(sec float64) string {
	ms := sec * 1000
	switch {
	case ms >= 1000:
		return strconv.FormatFloat(ms/1000, 'f', 2, 64)
	case ms < 10:
		return strconv.FormatFloat(ms, 'f', 1, 64)
	default:
		return strconv.FormatFloat(ms, 'f', 0, 64)
	}
}

func fmtMsUnit(sec float64) string {
	if sec*1000 >= 1000 {
		return "s"
	}
	return "ms"
}

// fmtMsFull is the combined "value unit" form for table cells ("88 ms").
func fmtMsFull(sec float64) string { return fmtMsVal(sec) + " " + fmtMsUnit(sec) }

// fmtCores renders an average-cores-consumed value as "0.87 cores" (two decimals).
func fmtCores(c float64) string { return strconv.FormatFloat(c, 'f', 2, 64) + " cores" }

// fmtBytes renders a per-request average byte size human-readably: <1KiB -> "128 B",
// <1MiB -> "1.9 KB", else "3.2 MB". The input is a float average (sum/count), so
// sub-byte fractions round to whole bytes. Uses 1024-based units, dropping a
// trailing ".0" so cells read cleanly ("2 KB" not "2.0 KB").
func fmtBytes(avg float64) string {
	switch {
	case avg >= 1<<30:
		return strings.TrimSuffix(strconv.FormatFloat(avg/(1<<30), 'f', 2, 64), ".00") + " GB"
	case avg >= 1<<20:
		return strings.TrimSuffix(strconv.FormatFloat(avg/(1<<20), 'f', 1, 64), ".0") + " MB"
	case avg >= 1<<10:
		return strings.TrimSuffix(strconv.FormatFloat(avg/(1<<10), 'f', 1, 64), ".0") + " KB"
	default:
		return strconv.FormatFloat(avg, 'f', 0, 64) + " B"
	}
}

// fmtCompact renders a large count with a k/M/bn suffix (4400 -> "4.4k",
// 1_200_000 -> "1.2M"), trimming a trailing ".0". Values below 1000 render as a
// plain integer. Used for the request/summary counts on the performance page,
// where the raw magnitudes are too large to read digit-by-digit.
func fmtCompact(f float64) string {
	switch {
	case f >= 1e9:
		return strings.TrimSuffix(strconv.FormatFloat(f/1e9, 'f', 1, 64), ".0") + "bn"
	case f >= 1e6:
		return strings.TrimSuffix(strconv.FormatFloat(f/1e6, 'f', 1, 64), ".0") + "M"
	case f >= 1e3:
		return strings.TrimSuffix(strconv.FormatFloat(f/1e3, 'f', 1, 64), ".0") + "k"
	default:
		return strconv.FormatFloat(f, 'f', 0, 64)
	}
}

// ------------------------------------------------------------- colour classes

// errClass colours an error-rate value: >=5% error, >=2% warn, else muted.
func errClass(f float64) string {
	switch {
	case f >= 0.05:
		return "c-err"
	case f >= 0.02:
		return "c-warn"
	default:
		return "c-txt2"
	}
}

// p99Class flags a slow p99 (>=600ms) so the cell reads warn.
func p99Class(sec float64) string {
	if sec >= 0.6 {
		return "c-warn"
	}
	return "c-txt2"
}

// fmtSpread renders the latency spread P95/P50 as e.g. "1.07×"; a non-positive
// P50 or P95 yields an em-dash, since the spread is undefined without both.
func fmtSpread(p50, p95 float64) string {
	if p50 <= 0 || p95 <= 0 {
		return "—"
	}
	return strconv.FormatFloat(p95/p50, 'f', 2, 64) + "×"
}

// spreadClass flags an elevated latency spread (>=2.5×) with the same warn colour
// as p99Class; a tight or undefined spread stays neutral (c-txt).
func spreadClass(p50, p95 float64) string {
	if p50 <= 0 || p95 <= 0 {
		return "c-txt"
	}
	if p95/p50 >= 2.5 {
		return "c-warn"
	}
	return "c-txt"
}

// healthClass picks the service health dot from its error rate.
func healthClass(f float64) string {
	switch {
	case f >= 0.05:
		return "dot-err"
	case f >= 0.02:
		return "dot-warn"
	default:
		return "dot-ok"
	}
}

// methodClass maps an HTTP method to its chip colour class.
func methodClass(m string) string {
	switch m {
	case "GET":
		return "m-get"
	case "POST":
		return "m-post"
	case "DELETE":
		return "m-delete"
	case "PUT":
		return "m-put"
	case "PATCH":
		return "m-patch"
	default:
		return "m-other"
	}
}

// codeClass colours an exact status code by its class.
func codeClass(code uint32) string {
	switch {
	case code >= 200 && code < 300:
		return "c-ok"
	case code < 400:
		return "c-blue"
	case code < 500:
		return "c-warn"
	default:
		return "c-err"
	}
}

// classBarClass colours a status-breakdown bar fill by class.
func classBarClass(class string) string {
	switch class {
	case "2xx":
		return "bar-ok"
	case "3xx":
		return "bar-blue"
	case "4xx":
		return "bar-warn"
	case "5xx", "no_status":
		return "bar-err"
	default:
		return "bar-txt3"
	}
}
