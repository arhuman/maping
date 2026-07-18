package web

// tplDetailHTML holds the level-3 endpoint-detail page: debug context, RED
// KPIs, the inline SVG charts, and every drill-down panel.
const tplDetailHTML = `
{{define "detail"}}<!doctype html>
<html lang="en"><head>{{template "head"}}<title>mAPI-ng — {{.Method}} {{.Route}}</title></head>
<body><div class="app">{{template "sidebar" .Shell}}<main class="main">{{template "topbar" .Shell}}
<div class="scrollbody"><div class="fade">
  <div style="display:flex;align-items:center;gap:12px;margin-bottom:20px;"><span class="chip {{mcls .Method}}" style="font-size:12px;padding:5px 10px;border-radius:7px;">{{.Method}}</span><span style="font:600 18px var(--mono);">{{.Route}}</span></div>
  <div class="panel" style="padding:13px 18px;margin-bottom:20px;display:flex;align-items:center;gap:12px;">
    <span class="dot {{.Verdict.DotClass}}" style="flex-shrink:0;"></span>
    <span style="font:700 14px var(--ui);flex-shrink:0;">{{.Verdict.Headline}}</span>
    {{if .Verdict.Qualifier}}<span style="font:600 10.5px var(--mono);color:var(--txt-3);border:1px solid var(--line);border-radius:6px;padding:2px 7px;flex-shrink:0;letter-spacing:.4px;">{{.Verdict.Qualifier}}</span>{{end}}
    <span style="font:500 12.5px var(--mono);color:var(--txt-3);">{{.Verdict.Sentence}}</span>
  </div>
  <div class="kpistrip" style="grid-template-columns:repeat(5,1fr);">{{range .Stats}}{{template "kpi" .}}{{end}}</div>
  <div style="display:grid;grid-template-columns:1.5fr 1fr;gap:18px;align-items:start;">
    <div class="panel" style="padding:18px 20px;">
      <div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:14px;"><span style="font-size:13.5px;font-weight:700;">Rate &amp; latency over time</span><div style="display:flex;gap:14px;"><span style="display:flex;align-items:center;gap:6px;font:600 11px var(--mono);color:var(--txt-2);"><span style="width:12px;height:3px;border-radius:2px;background:var(--accent);"></span>rate</span><span style="display:flex;align-items:center;gap:6px;font:600 11px var(--mono);color:var(--txt-2);"><span style="width:12px;height:3px;border-radius:2px;background:var(--violet);"></span>p95</span><span style="display:flex;align-items:center;gap:6px;font:600 11px var(--mono);color:var(--txt-2);"><span style="width:12px;height:3px;border-radius:2px;background:var(--err);"></span>errors</span></div></div>
      {{.TSChart}}
      <div style="display:flex;align-items:center;justify-content:space-between;gap:12px;margin-top:12px;padding-top:12px;border-top:1px solid var(--line-soft);">
        <div style="display:flex;align-items:center;gap:8px;font:600 11px var(--mono);">
          {{if .Range.Custom}}<span style="color:var(--accent);">⤢ {{.Range.Label}}</span><a href="{{.Range.ResetHref}}" style="color:var(--txt-3);">reset</a>{{else}}<span style="color:var(--txt-3);">{{.Range.Label}} window</span>{{end}}
        </div>
        <div style="display:flex;align-items:center;gap:6px;font:600 11px var(--mono);">
          {{if .Range.PanLeftHref}}<a href="{{.Range.PanLeftHref}}" class="wbtn" title="pan back">←</a>{{end}}
          <a href="{{.Range.ZoomOutHref}}" class="wbtn" title="zoom out 2×">zoom out</a>
          {{if .Range.PanRightHref}}<a href="{{.Range.PanRightHref}}" class="wbtn" title="pan forward">→</a>{{end}}
        </div>
      </div>
    </div>
    <div class="panel" style="padding:18px 20px;">
      <div style="font-size:13.5px;font-weight:700;margin-bottom:6px;">Status breakdown</div>
      <div style="font:500 11px var(--mono);color:var(--txt-3);margin-bottom:16px;">error = 4xx + 5xx + timeout</div>
      {{range .StatusBars}}
      <div style="margin-bottom:13px;">
        <div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:5px;">
          <span style="display:flex;align-items:center;gap:8px;font:600 12px var(--mono);" class="{{.LabelClass}}"><span style="width:8px;height:8px;border-radius:2px;" class="{{.BarClass}}"></span>{{.Label}}</span>
          <span style="font:600 12px var(--mono);color:var(--txt-2);">{{.Count}}</span>
        </div>
        <div style="height:6px;background:var(--panel-3);border-radius:4px;overflow:hidden;"><div style="height:100%;width:{{.Pct}};border-radius:4px;" class="{{.BarClass}}"></div></div>
      </div>
      {{end}}
      {{if .Detail.Codes}}
      <div style="margin-top:18px;padding-top:16px;border-top:1px solid var(--line);">
        <div style="font:600 10px var(--mono);color:var(--txt-3);letter-spacing:.8px;margin-bottom:10px;">EXACT CODES</div>
        <div style="display:flex;flex-wrap:wrap;gap:8px;">
          {{range .Detail.Codes}}<span style="font:600 11px var(--mono);color:var(--txt-2);background:var(--panel-3);border:1px solid var(--line);padding:4px 9px;border-radius:7px;"><span class="{{codec .Code}}">{{.Code}}</span> · {{.Count}}</span>{{end}}
        </div>
      </div>
      {{end}}
    </div>
  </div>
  <details class="diag"{{if .Verdict.Open}} open{{end}}>
  <summary>Diagnostic details</summary>
  <div class="panel" style="padding:13px 16px;margin-top:18px;display:flex;align-items:center;gap:14px;flex-wrap:wrap;">
    <span style="font:700 10px var(--mono);color:var(--txt-3);letter-spacing:1px;flex-shrink:0;">DEBUG CONTEXT</span>
    <span id="mp-debug" class="mono" style="flex:1;min-width:220px;font-size:12px;color:var(--txt-2);word-break:break-word;">{{.Debug.Summary}}</span>
    <button type="button" data-copy="mp-debug" style="flex-shrink:0;padding:5px 11px;border:1px solid var(--line);border-radius:7px;background:var(--panel-3);color:var(--txt-2);font:600 11px var(--mono);cursor:pointer;">⧉ copy</button>
  </div>
  <div class="panel" style="padding:18px 20px;margin-top:18px;">
    <div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:6px;"><span style="font-size:13.5px;font-weight:700;">Latency distribution</span><span style="font:500 11px var(--mono);color:var(--txt-3);">DDSketch · γ=1.01 · ~1% relative error</span></div>
    <div style="font:500 11px var(--mono);color:var(--txt-3);margin-bottom:8px;">merged bucket counts · log-spaced latency</div>
    {{.HistChart}}
  </div>
  {{if .Instances}}
  <div class="panel" style="overflow:hidden;margin-top:18px;">
    <div style="padding:16px 20px 4px;"><span style="font-size:13.5px;font-weight:700;">Instances</span><span style="font:500 11px var(--mono);color:var(--txt-3);margin-left:10px;">is a degradation one replica, or fleet-wide?</span></div>
    <div class="thead" style="grid-template-columns:2fr 1fr 1fr .8fr .8fr .8fr .9fr .9fr;">
      <span>INSTANCE</span><span style="text-align:right;">REQUESTS</span><span style="text-align:right;">ERROR %</span><span style="text-align:right;">p50</span><span style="text-align:right;">p95</span><span style="text-align:right;">p99</span><span style="text-align:right;">AVG REQ</span><span style="text-align:right;">AVG RESP</span>
    </div>
    {{range .Instances}}
    <div class="trow" style="grid-template-columns:2fr 1fr 1fr .8fr .8fr .8fr .9fr .9fr;cursor:default;">
      <div style="font:500 13px var(--mono);white-space:nowrap;overflow:hidden;text-overflow:ellipsis;">{{if .IsOutlier}}<span class="dot dot-warn" style="margin-right:7px;"></span>{{end}}{{.Instance}}</div>
      <div class="tnum">{{.Count}}</div>
      <div class="tnum {{errc .ErrorRate}}">{{pctd .ErrorRate}}</div>
      <div class="tnum-s">{{msf .P50}}</div>
      <div class="tnum-s{{if .IsOutlier}} c-warn{{end}}">{{msf .P95}}</div>
      <div class="tnum-s {{p99c .P99}}">{{msf .P99}}</div>
      <div class="tnum-s">{{bytes .ReqBytesAvg}}</div>
      <div class="tnum-s">{{bytes .RespBytesAvg}}</div>
    </div>
    {{end}}
  </div>
  {{end}}
  {{if .Versions}}
  <div class="panel" style="overflow:hidden;margin-top:18px;">
    <div style="padding:16px 20px 4px;"><span style="font-size:13.5px;font-weight:700;">Versions</span><span style="font:500 11px var(--mono);color:var(--txt-3);margin-left:10px;">did a release regress this endpoint?</span></div>
    <div class="thead" style="grid-template-columns:2fr 1fr 1fr .8fr .8fr .8fr;">
      <span>VERSION</span><span style="text-align:right;">REQUESTS</span><span style="text-align:right;">ERROR %</span><span style="text-align:right;">p50</span><span style="text-align:right;">p95</span><span style="text-align:right;">p99</span>
    </div>
    {{range .Versions}}
    <div class="trow" style="grid-template-columns:2fr 1fr 1fr .8fr .8fr .8fr;cursor:default;">
      <div style="font:500 13px var(--mono);white-space:nowrap;overflow:hidden;text-overflow:ellipsis;">{{.Version}}</div>
      <div class="tnum">{{.Count}}</div>
      <div class="tnum {{errc .ErrorRate}}">{{pctd .ErrorRate}}</div>
      <div class="tnum-s">{{msf .P50}}</div>
      <div class="tnum-s">{{msf .P95}}</div>
      <div class="tnum-s {{p99c .P99}}">{{msf .P99}}</div>
    </div>
    {{end}}
  </div>
  {{end}}
  {{if .Exemplars}}
  <div class="panel" style="overflow:hidden;margin-top:18px;">
    <div style="padding:16px 20px 4px;"><span style="font-size:13.5px;font-weight:700;">Exemplars</span><span style="font:500 11px var(--mono);color:var(--txt-3);margin-left:10px;">jump from a spike to a real request</span></div>
    <div class="thead" style="grid-template-columns:.9fr .7fr .8fr 1.6fr 1.6fr;">
      <span>TIME</span><span style="text-align:right;">STATUS</span><span style="text-align:right;">LATENCY</span><span style="padding-left:16px;">TRACE ID</span><span>REQUEST ID</span>
    </div>
    {{range $i, $e := .Exemplars}}
    <div class="trow" style="grid-template-columns:.9fr .7fr .8fr 1.6fr 1.6fr;cursor:default;">
      <div style="font:500 12.5px var(--mono);color:var(--txt-2);">{{$e.Time}}</div>
      <div class="tnum {{codec $e.StatusCode}}">{{$e.StatusCode}}</div>
      <div class="tnum-s">{{msf $e.Latency}}</div>
      <div style="display:flex;align-items:center;gap:8px;min-width:0;padding-left:16px;">
        {{if $e.FullTrace}}
        <span class="mono" style="font-size:12px;color:var(--txt-2);white-space:nowrap;overflow:hidden;text-overflow:ellipsis;">{{$e.ShortTrace}}</span>
        <span id="mp-ex-tr-{{$i}}" style="position:absolute;width:1px;height:1px;overflow:hidden;clip:rect(0 0 0 0);">{{$e.FullTrace}}</span>
        <button type="button" data-copy="mp-ex-tr-{{$i}}" style="flex-shrink:0;padding:2px 7px;border:1px solid var(--line);border-radius:6px;background:var(--panel-3);color:var(--txt-3);font:600 10px var(--mono);cursor:pointer;">⧉</button>
        {{else}}<span class="mono" style="font-size:12px;color:var(--txt-3);">—</span>{{end}}
      </div>
      <div style="display:flex;align-items:center;gap:8px;min-width:0;">
        {{if $e.FullReq}}
        <span class="mono" style="font-size:12px;color:var(--txt-2);white-space:nowrap;overflow:hidden;text-overflow:ellipsis;">{{$e.ShortReq}}</span>
        <span id="mp-ex-rq-{{$i}}" style="position:absolute;width:1px;height:1px;overflow:hidden;clip:rect(0 0 0 0);">{{$e.FullReq}}</span>
        <button type="button" data-copy="mp-ex-rq-{{$i}}" style="flex-shrink:0;padding:2px 7px;border:1px solid var(--line);border-radius:6px;background:var(--panel-3);color:var(--txt-3);font:600 10px var(--mono);cursor:pointer;">⧉</button>
        {{else}}<span class="mono" style="font-size:12px;color:var(--txt-3);">—</span>{{end}}
      </div>
    </div>
    {{end}}
  </div>
  {{end}}
  {{if .ClassLatency}}
  <div class="panel" style="overflow:hidden;margin-top:18px;">
    <div style="padding:16px 20px 4px;"><span style="font-size:13.5px;font-weight:700;">Latency by status class</span><span style="font:500 11px var(--mono);color:var(--txt-3);margin-left:10px;">is the latency on failures, or also on successes?</span></div>
    <div class="thead" style="grid-template-columns:1.4fr 1fr 1fr 1fr 1fr;">
      <span>CLASS</span><span style="text-align:right;">REQUESTS</span><span style="text-align:right;">p50</span><span style="text-align:right;">p95</span><span style="text-align:right;">p99</span>
    </div>
    {{range .ClassLatency}}
    <div class="trow" style="grid-template-columns:1.4fr 1fr 1fr 1fr 1fr;cursor:default;">
      <div style="display:flex;align-items:center;gap:8px;font:600 12.5px var(--mono);"><span style="width:8px;height:8px;border-radius:2px;" class="{{barc .Class}}"></span>{{.Class}}</div>
      <div class="tnum">{{.Count}}</div>
      <div class="tnum-s">{{msf .P50}}</div>
      <div class="tnum-s">{{msf .P95}}</div>
      <div class="tnum-s {{p99c .P99}}">{{msf .P99}}</div>
    </div>
    {{end}}
  </div>
  {{end}}
  <div class="panel" style="padding:18px 20px;margin-top:18px;">
    <div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:6px;"><span style="font-size:13.5px;font-weight:700;">Self vs downstream</span><span style="font:500 11px var(--mono);color:var(--txt-3);">where does the time go?</span></div>
    {{if .Downstream.HasData}}
    <div style="display:flex;gap:26px;margin:12px 0 14px;">
      <div><div style="font:600 10px var(--mono);color:var(--txt-3);letter-spacing:.6px;">SELF (avg)</div><div style="font:700 18px var(--mono);color:var(--txt);">{{msf .Downstream.SelfSeconds}}</div></div>
      <div><div style="font:600 10px var(--mono);color:var(--txt-3);letter-spacing:.6px;">DOWNSTREAM (avg)</div><div style="font:700 18px var(--mono);color:var(--accent);">{{msf .Downstream.DownSeconds}}</div></div>
      <div><div style="font:600 10px var(--mono);color:var(--txt-3);letter-spacing:.6px;">DOWNSTREAM SHARE</div><div style="font:700 18px var(--mono);color:var(--txt);">{{pctd .Downstream.DownFraction}}</div></div>
    </div>
    <div style="display:flex;height:12px;border-radius:6px;overflow:hidden;background:var(--panel-3);">
      <div style="width:{{.Downstream.SelfWidth}};background:var(--txt-3);"></div>
      <div style="width:{{.Downstream.DownWidth}};background:var(--accent);"></div>
    </div>
    <div style="display:flex;justify-content:space-between;margin-top:8px;font:500 11px var(--mono);color:var(--txt-3);"><span><span style="color:var(--txt-3);">■</span> self · endpoint's own work</span><span>downstream · outbound waits <span class="c-accent">■</span></span></div>
    {{else}}
    <div style="padding:14px 0 2px;color:var(--txt-3);font-size:13px;">No downstream timing in this window. Set <span style="font:600 12px var(--mono);color:var(--txt-2);">maping.NewRoundTripper</span> as the Transport of your outbound http.Client to split self vs downstream time.</div>
    {{end}}
  </div>
  {{if .ErrorClasses}}
  <div class="panel" style="overflow:hidden;margin-top:18px;">
    <div style="padding:16px 20px 4px;"><span style="font-size:13.5px;font-weight:700;">Error classes</span><span style="font:500 11px var(--mono);color:var(--txt-3);margin-left:10px;">what is behind the errors?</span></div>
    <div class="thead" style="grid-template-columns:3fr 1fr;">
      <span>CLASS</span><span style="text-align:right;">REQUESTS</span>
    </div>
    {{range .ErrorClasses}}
    <div class="trow" style="grid-template-columns:3fr 1fr;cursor:default;">
      <div style="font:600 12.5px var(--mono);color:var(--txt-2);white-space:nowrap;overflow:hidden;text-overflow:ellipsis;">{{.Class}}</div>
      <div class="tnum">{{.Count}}</div>
    </div>
    {{end}}
  </div>
  {{end}}
  {{if .NoStatusReasons}}
  <div class="panel" style="overflow:hidden;margin-top:18px;">
    <div style="padding:16px 20px 4px;"><span style="font-size:13.5px;font-weight:700;">No-status reasons</span><span style="font:500 11px var(--mono);color:var(--txt-3);margin-left:10px;">timing out, canceling, or crashing?</span></div>
    <div class="thead" style="grid-template-columns:3fr 1fr;">
      <span>REASON</span><span style="text-align:right;">REQUESTS</span>
    </div>
    {{range .NoStatusReasons}}
    <div class="trow" style="grid-template-columns:3fr 1fr;cursor:default;">
      <div style="font:600 12.5px var(--mono);color:var(--txt-2);">{{.Reason}}</div>
      <div class="tnum">{{.Count}}</div>
    </div>
    {{end}}
  </div>
  {{end}}
  {{if .MemoryVerdict.Show}}
  <div class="panel" style="padding:18px 20px;margin-top:18px;">
    <div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:4px;"><span style="font-size:13.5px;font-weight:700;">Memory</span><span style="font:500 11px var(--mono);color:var(--txt-3);">leak or burst? · the instances serving this endpoint, per-process</span></div>
    <div style="display:flex;align-items:center;gap:11px;margin:10px 0 14px;">
      <span class="dot {{.MemoryVerdict.DotClass}}" style="flex-shrink:0;"></span>
      <span style="font:700 13.5px var(--ui);flex-shrink:0;">{{.MemoryVerdict.Level}}</span>
      {{if .MemoryVerdict.Confidence}}<span style="font:600 10px var(--mono);color:var(--txt-3);border:1px solid var(--line);border-radius:6px;padding:2px 7px;flex-shrink:0;letter-spacing:.4px;">{{.MemoryVerdict.Confidence}} confidence</span>{{end}}
      <span style="font:500 12.5px var(--mono);color:var(--txt-2);">{{.MemoryVerdict.Sentence}}</span>
    </div>
    {{.MemoryChart}}
    <div style="display:flex;gap:16px;margin-top:12px;font:600 11px var(--mono);color:var(--txt-3);">
      <span style="display:flex;align-items:center;gap:6px;"><span style="width:12px;height:3px;border-radius:2px;background:var(--accent);"></span>post-GC live heap</span>
      <span style="display:flex;align-items:center;gap:6px;"><span style="width:12px;height:3px;border-radius:2px;background:var(--txt-3);"></span>in-use heap</span>
    </div>
    {{if .MemoryVerdict.Evidence}}
    <ul style="margin:14px 0 0;padding-left:18px;display:flex;flex-direction:column;gap:6px;">
      {{range .MemoryVerdict.Evidence}}<li style="font:500 12px var(--mono);color:var(--txt-2);">{{.}}</li>{{end}}
    </ul>
    {{end}}
    {{if .MemoryVerdict.Falsifier}}
    <div style="margin-top:12px;padding-top:12px;border-top:1px solid var(--line-soft);font:500 11.5px var(--mono);color:var(--txt-3);"><span style="color:var(--txt-2);">Falsifier:</span> {{.MemoryVerdict.Falsifier}}</div>
    {{end}}
  </div>
  {{end}}
  {{if .Resources}}
  <div class="panel" style="overflow:hidden;margin-top:18px;">
    <div style="padding:16px 20px 4px;"><span style="font-size:13.5px;font-weight:700;">Resources</span><span style="font:500 11px var(--mono);color:var(--txt-3);margin-left:10px;">saturation per instance — GC or goroutines behind a slowdown?</span></div>
    <div class="thead" style="grid-template-columns:2fr 1fr 1fr 1fr 1fr 1fr 1fr 1fr 1fr 1fr 1fr 1fr;">
      <span>INSTANCE</span><span style="text-align:right;">CPU</span><span style="text-align:right;">GC</span><span style="text-align:right;">RSS</span><span style="text-align:right;">HEAP</span><span style="text-align:right;">GOROUTINES</span><span style="text-align:right;">GC/s</span><span style="text-align:right;">GC CPU</span><span style="text-align:right;">ALLOC/s</span><span style="text-align:right;">AVG ALLOC</span><span style="text-align:right;">POST-GC HEAP</span><span style="text-align:right;">TRUE RSS</span>
    </div>
    {{range .Resources}}
    <div class="trow" style="grid-template-columns:2fr 1fr 1fr 1fr 1fr 1fr 1fr 1fr 1fr 1fr 1fr 1fr;cursor:default;">
      <div style="font:500 13px var(--mono);white-space:nowrap;overflow:hidden;text-overflow:ellipsis;">{{.Instance}}</div>
      <div class="tnum-s">{{fmtCores .CoresUsed}}</div>
      <div class="tnum-s">{{pctd .GCShare}}</div>
      <div class="tnum-s">{{bytes .RSSBytes}}</div>
      <div class="tnum-s">{{bytes .HeapBytes}}</div>
      <div class="tnum">{{.Goroutines}}</div>
      <div class="tnum-s">{{rated .GCFreq}}/s</div>
      <div class="tnum-s">{{pctd .GCCPUFraction}}</div>
      <div class="tnum-s">{{bytes .AllocRate}}/s</div>
      <div class="tnum-s">{{bytes .AvgAllocSize}}</div>
      <div class="tnum-s">{{bytes .PostGCHeap}}</div>
      <div class="tnum-s">{{if .HasTrueRSS}}{{bytes .TrueRSSBytes}}{{else}}—{{end}}</div>
    </div>
    {{end}}
  </div>
  {{end}}
  </details>
</div></div>
</main></div>
<script src="/assets/copy.js" defer></script>
</body></html>{{end}}
`
