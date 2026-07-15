package web

import (
	"net/http"
	"strconv"
	"time"
)

// window is the fixed dashboard lookback. The 3-level view is a live RED pane,
// not a range picker (CONTEXT: fixed, non-configurable dashboard), so a single
// window keeps the UI simple and the tier selection deterministic.
const window = time.Hour

// seriesStep is the default time-series bucket width for the detail chart at a
// preset window. A custom (zoomed) range derives its own step via adaptiveStep.
const seriesStep = time.Minute

// resolveDetailRange returns the detail-page time range. A valid ?from=&to= pair
// (unix seconds, to>from, at least minDetailSpan apart, trailing edge clamped to
// now) overrides the preset window and marks the range custom; anything missing or
// malformed falls back to the preset windowRange(dur). This is the guard: only a
// well-formed, non-future, non-degenerate range ever drives a custom query.
func resolveDetailRange(r *http.Request, dur time.Duration) (from, to time.Time, custom bool) {
	if f, t, ok := customRange(r); ok {
		return f, t, true
	}
	from, to = windowRange(dur)
	return from, to, false
}

// customRange parses a well-formed ?from=&to= pair (unix seconds) into a
// non-future, non-degenerate UTC range. It returns ok=false — via guard clauses,
// so the caller falls back to the preset window — when the params are missing,
// unparseable, or narrower than minDetailSpan.
func customRange(r *http.Request) (from, to time.Time, ok bool) {
	fs, ts := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	if fs == "" || ts == "" {
		return time.Time{}, time.Time{}, false
	}
	fi, e1 := strconv.ParseInt(fs, 10, 64)
	ti, e2 := strconv.ParseInt(ts, 10, 64)
	if e1 != nil || e2 != nil {
		return time.Time{}, time.Time{}, false
	}
	f, t := time.Unix(fi, 0).UTC(), time.Unix(ti, 0).UTC()
	if now := time.Now().UTC(); t.After(now) {
		t = now
	}
	if t.Sub(f) < minDetailSpan {
		return time.Time{}, time.Time{}, false
	}
	return f, t, true
}

// minDetailSpan is the tightest custom range allowed: below the raw tier's 10s
// resolution there is no finer data to show, so a narrower request is rejected.
const minDetailSpan = 10 * time.Second

// adaptiveStep picks a series bucket width for a custom range so the chart keeps a
// readable ~detailBuckets points, floored at the raw tier's 10s resolution (no
// finer data exists). Preset windows keep the fixed seriesStep for parity.
func adaptiveStep(span time.Duration) time.Duration {
	step := span / detailBuckets
	if step < 10*time.Second {
		return 10 * time.Second
	}
	return step
}

// detailBuckets is the target point count for a custom-range series chart.
const detailBuckets = 60
