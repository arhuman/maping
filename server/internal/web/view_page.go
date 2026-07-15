package web

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/arhuman/maping/server/internal/storage"
)

// bytesPerRawEvent is the assumed on-disk size of ONE request in a raw-event
// pipeline (a structured access-log / span row). mAPI-ng stores no raw events, so
// the "raw-event pipeline" side of the disk comparison cannot be measured — it is
// projected from the real represented-request count times this documented
// constant, and the footnote states the assumption so the number is honest.
const bytesPerRawEvent = 300

// rollupTier is one row of the Performance "Rollup tiers" panel: the tier's
// resolution, its retention window, and a bar width proportional to the number of
// windows the tier retains — the real relative on-disk volume, not an eyeballed
// taper.
type rollupTier struct {
	Res       string // resolution label, e.g. "10s", "1min"
	Retention string // retention label, e.g. "raw · 7 days", "30 days"
	BarPct    string // CSS width, proportional to retained-window count
}

// rollupTiers mirrors the real cascading tiers and their retention TTLs from
// migration 0002_rollups.sql (raw/1m/1h/1d retained 7/30/180/730 days). The bar
// width is the count of windows each tier keeps (retentionDays*86400 /
// resolutionSec) normalised to the widest (raw) tier, so the taper shows the true
// per-tier volume — coarser tiers hold dramatically fewer rows — rather than
// hardcoded percentages. A CSS min-width keeps sub-percent tiers visible.
func rollupTiers() []rollupTier {
	specs := []struct {
		res, retention  string
		resSec, retDays float64
	}{
		{"10s", "raw · 7 days", 10, 7},
		{"1min", "30 days", 60, 30},
		{"1hr", "180 days", 3600, 180},
		{"1day", "730 days", 86400, 730},
	}
	windows := make([]float64, len(specs))
	var maxWin float64
	for i, s := range specs {
		windows[i] = s.retDays * 86400 / s.resSec
		if windows[i] > maxWin {
			maxWin = windows[i]
		}
	}
	out := make([]rollupTier, len(specs))
	for i, s := range specs {
		pct := 100.0
		if maxWin > 0 {
			pct = windows[i] / maxWin * 100
		}
		out[i] = rollupTier{
			Res:       s.res,
			Retention: s.retention,
			BarPct:    strconv.FormatFloat(pct, 'f', 1, 64) + "%",
		}
	}
	return out
}

// buildPerformance turns the real, tenant-scoped PerformanceStat into the display
// strings for the performance page. Requests/summaries/compression and the
// summaries disk size are measured; the raw-event-pipeline size is projected from
// the request count and bytesPerRawEvent (see above). window is the selected
// lookback the stat was measured over (for the window-average ingest rate and the
// volume labels), winKey its allowlisted key for the chip label, and queryDur the
// measured latency of the stat query itself, surfaced as the query-latency KPI.
func buildPerformance(shell Shell, s storage.PerformanceStat, window, queryDur time.Duration, winKey string) performancePage {
	p := performancePage{
		Shell:         shell,
		QueryMs:       fmt.Sprintf("%.0f", float64(queryDur.Microseconds())/1000),
		RawEventBytes: bytesPerRawEvent,
		Tiers:         rollupTiers(),
		WindowLabel:   windowText[winKey],
		WindowShort:   strings.ToUpper(winKey),
	}
	if s.Requests == 0 || s.Summaries == 0 {
		return p // HasData stays false: the template shows a waiting-for-data state.
	}
	p.HasData = true

	rawBytes := float64(s.Requests) * bytesPerRawEvent
	summaryBytes := float64(s.SummaryDiskBytes)
	secs := window.Seconds()

	p.Requests = fmtCompact(float64(s.Requests))
	p.Summaries = fmtCompact(float64(s.Summaries))
	p.Compression = fmtCompact(float64(s.Requests)/float64(s.Summaries)) + "×"
	if secs > 0 {
		p.IngestRate = fmtCompact(float64(s.Requests)/secs) + "/s"
	}
	p.RawDisk = fmtBytes(rawBytes)
	p.SummaryDisk = fmtBytes(summaryBytes)

	// Bar width and ratio: guard the degenerate case where the estimated summary
	// size meets or exceeds the projected raw size (very low traffic, where a
	// summary's fixed sketch overhead is not yet amortised) — then there is no
	// reduction to claim, so the bar fills and the ratio reads "—".
	if summaryBytes > 0 && rawBytes > summaryBytes {
		pct := summaryBytes / rawBytes * 100
		if pct < 0.5 {
			pct = 0.5
		}
		p.SummaryBarPct = strconv.FormatFloat(pct, 'f', 1, 64) + "%"
		p.Ratio = fmtCompact(rawBytes/summaryBytes) + "×"
	} else {
		p.SummaryBarPct = "100%"
		p.Ratio = "—"
	}
	return p
}

// keyRow is one rendered row of the Setup keys table: the label, a masked
// last-4 fragment, the issue date, and whether the key is revoked (revoked keys
// drop the revoke action).
type keyRow struct {
	ID      string
	Label   string
	Masked  string // "····<last4>", the only fragment of the secret we can show
	Created string // date only; the list is a ledger, not a timeline
	Revoked bool
}

// toKeyRows maps the control-plane key infos into display rows, masking the
// last-4 and formatting the issue date. Order is preserved (newest first).
func toKeyRows(infos []KeyInfo) []keyRow {
	out := make([]keyRow, 0, len(infos))
	for _, k := range infos {
		out = append(out, keyRow{
			ID:      k.ID,
			Label:   k.Label,
			Masked:  "····" + k.Last4,
			Created: k.CreatedAt.Format("2006-01-02"),
			Revoked: k.RevokedAt != nil,
		})
	}
	return out
}

// onboardingStep is one of the 4 CONTEXT onboarding steps with its done state
// and a short human label.
type onboardingStep struct {
	Label string
	Done  bool
}

// onboardingData drives the onboarding panel template: the 4 steps, the list of
// connected sources (if any), and the frozen-cardinality warning.
type onboardingData struct {
	Steps     []onboardingStep
	Connected []ServiceOnboarding
	Frozen    bool
}

// buildOnboarding derives the live 4-step state (CONTEXT Handshake) from the
// handshake list and the frozen flag:
//
//	step 1 key valid          — always true here (this page is only reachable
//	                            after the tenant resolved, i.e. a valid key);
//	step 2 service connected  — at least one handshake row exists;
//	step 3 waiting for Summary — a service connected but no summary yet (this
//	                            panel is only shown when the tenant has NO data,
//	                            so once connected we are, by definition, waiting);
//	step 4 first data received — false here (renderOnboarding is only reached
//	                            when HasAnySummary is false).
//
// It never invents data: clock-skew drops and bad-key rejections are surfaced
// only where a real per-tenant signal exists; there is no per-tenant skew
// counter yet, so that line is omitted rather than faked (Part-2 follow-up).
func buildOnboarding(connected []ServiceOnboarding, frozen bool) onboardingData {
	serviceConnected := len(connected) > 0
	return onboardingData{
		Steps: []onboardingStep{
			{Label: "Ingest key valid", Done: true},
			{Label: "Service connected", Done: serviceConnected},
			{Label: "Waiting for first Summary", Done: serviceConnected},
			{Label: "First data received", Done: false},
		},
		Connected: connected,
		Frozen:    frozen,
	}
}
