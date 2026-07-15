package web

// tplPerformanceHTML holds the performance/architecture page: compression KPIs,
// the disk comparison, the rollup-tier bars, and the ingestion-path diagram.
const tplPerformanceHTML = `
{{define "performance"}}<!doctype html>
<html lang="en"><head>{{template "head"}}<title>mAPI-ng — performance</title></head>
<body><div class="app">{{template "sidebar" .Shell}}<main class="main">{{template "topbar" .Shell}}
<div class="scrollbody"><div style="max-width:1080px;" class="fade">
  <p style="font-size:14px;color:var(--txt-2);max-width:620px;margin:0 0 26px;line-height:1.6;">mAPI-ng aggregates in-process into DDSketch summaries before shipping — so one small record can represent thousands of requests. More data per second ingested, far less disk used, faster queries than raw-event pipelines.</p>
  <div class="kpistrip" style="grid-template-columns:repeat(4,1fr);">
    <div class="kpi"><div class="kpi-l">REQUESTS · {{.WindowShort}}</div><div class="kpi-v"><span class="kpi-num c-txt" style="font-size:30px;">{{if .HasData}}{{.Requests}}{{else}}—{{end}}</span></div><div class="kpi-sub">represented by your stored summaries</div></div>
    <div class="kpi"><div class="kpi-l">SUMMARIES · {{.WindowShort}}</div><div class="kpi-v"><span class="kpi-num c-txt" style="font-size:30px;">{{if .HasData}}{{.Summaries}}{{else}}—{{end}}</span></div><div class="kpi-sub">records actually shipped &amp; stored</div></div>
    <div class="kpi"><div class="kpi-l">COMPRESSION</div><div class="kpi-v"><span class="kpi-num c-accent" style="font-size:30px;">{{if .HasData}}{{.Compression}}{{else}}—{{end}}</span></div><div class="kpi-sub">requests per shipped summary</div></div>
    <div class="kpi"><div class="kpi-l">QUERY LATENCY</div><div class="kpi-v"><span class="kpi-num c-txt" style="font-size:30px;">{{.QueryMs}}</span><span class="kpi-u">ms</span></div><div class="kpi-sub">this page's aggregate query</div></div>
  </div>
  <div style="display:grid;grid-template-columns:1fr 1fr;gap:18px;">
    <div class="panel" style="padding:20px 22px;">
      <div style="font-size:14px;font-weight:700;margin-bottom:4px;">Disk · last {{.WindowLabel}}</div>
      <div style="font:500 11px var(--mono);color:var(--txt-3);margin-bottom:20px;">your real traffic{{if .HasData}} · raw side assumes ~{{.RawEventBytes}} B/event{{end}}</div>
      {{if .HasData}}
      <div style="display:flex;flex-direction:column;gap:18px;">
        <div><div style="display:flex;justify-content:space-between;margin-bottom:7px;font:600 12px var(--mono);"><span class="c-txt2">Raw-event pipeline <span style="color:var(--txt-3);">· projected</span></span><span class="c-err">{{.RawDisk}}</span></div><div style="height:12px;background:var(--panel-3);border-radius:6px;overflow:hidden;"><div style="height:100%;width:100%;background:linear-gradient(90deg,#FF6B6B,#c94a4a);border-radius:6px;"></div></div></div>
        <div><div style="display:flex;justify-content:space-between;margin-bottom:7px;font:600 12px var(--mono);"><span class="c-txt2">mAPI-ng summaries <span style="color:var(--txt-3);">· measured</span></span><span class="c-accent">{{.SummaryDisk}}</span></div><div style="height:12px;background:var(--panel-3);border-radius:6px;overflow:hidden;"><div style="height:100%;width:{{.SummaryBarPct}};min-width:5px;background:var(--accent);border-radius:6px;"></div></div></div>
      </div>
      <div style="margin-top:20px;padding-top:16px;border-top:1px solid var(--line);display:flex;align-items:baseline;gap:8px;"><span style="font:700 28px var(--mono);color:var(--accent);letter-spacing:-1px;">{{.Ratio}}</span><span style="font-size:12.5px;color:var(--txt-2);">less disk — {{.Requests}} requests carried by {{.Summaries}} summaries.</span></div>
      {{else}}
      <div style="padding:22px 0;color:var(--txt-3);font-size:13px;line-height:1.55;">No summaries stored in the last {{.WindowLabel}} yet. Once your service ships data, real ingest volume and disk savings appear here.</div>
      {{end}}
    </div>
    <div class="panel" style="padding:20px 22px;">
      <div style="font-size:14px;font-weight:700;margin-bottom:4px;">Rollup tiers</div>
      <div style="font:500 11px var(--mono);color:var(--txt-3);margin-bottom:20px;">counters sum · sketches merge bucket-wise (exact)</div>
      {{range .Tiers}}
      <div style="display:flex;align-items:center;gap:14px;margin-bottom:14px;"><div style="width:56px;font:700 14px var(--mono);color:var(--accent);">{{.Res}}</div><div style="flex:1;height:8px;background:var(--panel-3);border-radius:5px;overflow:hidden;"><div style="height:100%;width:{{.BarPct}};min-width:6px;background:var(--accent);border-radius:5px;"></div></div><div style="width:96px;text-align:right;font:500 11px var(--mono);color:var(--txt-3);">{{.Retention}}</div></div>
      {{end}}
      <div style="margin-top:16px;font-size:12px;color:var(--txt-3);line-height:1.5;">Fine tiers expire after rollup. Mergeability is what makes coarser tiers exact, not approximate.</div>
    </div>
  </div>
  <div class="panel" style="padding:22px;margin-top:18px;">
    <div style="font-size:14px;font-weight:700;margin-bottom:20px;">Ingestion path</div>
    <div style="display:flex;align-items:stretch;gap:0;">
      <div style="flex:1;display:flex;align-items:center;"><div style="flex:1;background:var(--panel-2);border:1px solid var(--line);border-radius:12px;padding:15px 14px;text-align:center;"><div style="font-size:19px;margin-bottom:8px;">⌨</div><div style="font-size:12.5px;font-weight:700;margin-bottom:4px;">Middleware</div><div style="font:500 10px var(--mono);color:var(--txt-3);line-height:1.4;">~20 lines, above Recovery</div></div><span class="c-accent" style="font-size:16px;padding:0 6px;">→</span></div>
      <div style="flex:1;display:flex;align-items:center;"><div style="flex:1;background:var(--panel-2);border:1px solid var(--line);border-radius:12px;padding:15px 14px;text-align:center;"><div style="font-size:19px;margin-bottom:8px;">◱</div><div style="font-size:12.5px;font-weight:700;margin-bottom:4px;">DDSketch</div><div style="font:500 10px var(--mono);color:var(--txt-3);line-height:1.4;">in-process aggregate</div></div><span class="c-accent" style="font-size:16px;padding:0 6px;">→</span></div>
      <div style="flex:1;display:flex;align-items:center;"><div style="flex:1;background:var(--panel-2);border:1px solid var(--line);border-radius:12px;padding:15px 14px;text-align:center;"><div style="font-size:19px;margin-bottom:8px;">↑</div><div style="font-size:12.5px;font-weight:700;margin-bottom:4px;">Summary</div><div style="font:500 10px var(--mono);color:var(--txt-3);line-height:1.4;">gRPC, one per flush</div></div><span class="c-accent" style="font-size:16px;padding:0 6px;">→</span></div>
      <div style="flex:1;display:flex;align-items:center;"><div style="flex:1;background:var(--panel-2);border:1px solid var(--line);border-radius:12px;padding:15px 14px;text-align:center;"><div style="font-size:19px;margin-bottom:8px;">▤</div><div style="font-size:12.5px;font-weight:700;margin-bottom:4px;">ClickHouse</div><div style="font:500 10px var(--mono);color:var(--txt-3);line-height:1.4;">row-level multitenant</div></div><span class="c-accent" style="font-size:16px;padding:0 6px;">→</span></div>
      <div style="flex:1;display:flex;align-items:center;"><div style="flex:1;background:var(--panel-2);border:1px solid var(--line);border-radius:12px;padding:15px 14px;text-align:center;"><div style="font-size:19px;margin-bottom:8px;">▦</div><div style="font-size:12.5px;font-weight:700;margin-bottom:4px;">Dashboard</div><div style="font:500 10px var(--mono);color:var(--txt-3);line-height:1.4;">RED, no config</div></div></div>
    </div>
  </div>
</div></div>
</main></div></body></html>{{end}}
`
