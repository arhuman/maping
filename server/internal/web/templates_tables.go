package web

// tplTablesHTML holds the level-1 service overview and the level-2 sortable
// endpoint table pages.
const tplTablesHTML = `
{{define "overview"}}<!doctype html>
<html lang="en"><head>{{template "head"}}{{if .Shell.Live}}<meta http-equiv="refresh" content="15">{{end}}<title>mAPI-ng — services</title></head>
<body><div class="app">{{template "sidebar" .Shell}}<main class="main">{{template "topbar" .Shell}}
<div class="scrollbody"><div class="fade">
  {{if .Frozen}}<div class="warnbar"><span style="font-size:15px;">⚠</span><span class="c-txt2"><b class="c-warn">Cardinality frozen.</b> New series are rejected for this tenant; existing series keep flowing. Raise the plan cap or reduce endpoint cardinality.</span></div>{{end}}
  <div class="kpistrip" style="grid-template-columns:repeat(4,1fr);">{{range .KPIs}}{{template "kpi" .}}{{end}}</div>
  <div class="panel" style="overflow:hidden;">
    <div class="thead" style="grid-template-columns:2fr 1fr 1fr .8fr .8fr .8fr;"><span>SERVICE</span><span style="text-align:right;">TRAFFIC</span><span style="text-align:right;">ERROR %</span><span style="text-align:right;">p50</span><span style="text-align:right;">p95</span><span style="text-align:right;">p99</span></div>
    {{range .Services}}
    <a class="trow" style="grid-template-columns:2fr 1fr 1fr .8fr .8fr .8fr;" href="{{.DrillHref}}">
      <div style="display:flex;align-items:center;gap:12px;"><span class="dot {{hc .ErrorRate}}"></span><div style="font-size:13.5px;font-weight:600;">{{.Service}}</div></div>
      <div class="tnum">{{rated .RatePerSec}}/s</div>
      <div class="tnum {{errc .ErrorRate}}">{{pctd .ErrorRate}}</div>
      <div class="tnum-s">{{msf .P50}}</div>
      <div class="tnum-s">{{msf .P95}}</div>
      <div class="tnum-s {{p99c .P99}}">{{msf .P99}}</div>
    </a>
    {{else}}
    <div style="padding:22px 20px;color:var(--txt-3);font-size:13px;">No services reporting in this window.</div>
    {{end}}
  </div>
  <div class="note">Aggregated from client-side DDSketch summaries · {{.WindowLabel}} window</div>
</div></div>
</main></div></body></html>{{end}}

{{define "endpoints"}}<!doctype html>
<html lang="en"><head>{{template "head"}}<title>mAPI-ng — {{.Service}}</title></head>
<body><div class="app">{{template "sidebar" .Shell}}<main class="main">{{template "topbar" .Shell}}
<div class="scrollbody"><div class="fade">
  {{if .Frozen}}<div class="warnbar"><span style="font-size:15px;">⚠</span><span class="c-txt2"><b class="c-warn">Cardinality frozen.</b> New series are rejected for this tenant.</span></div>{{end}}
  <div class="kpistrip" style="grid-template-columns:repeat(3,1fr);">{{range .KPIs}}{{template "kpi" .}}{{end}}</div>
  <div class="panel" style="overflow:hidden;">
    <div class="thead" style="grid-template-columns:2.2fr 1fr 1fr .7fr .7fr .7fr .8fr .8fr;">
      <span>ENDPOINT</span>
      <a href="?sort=traffic&win={{.Shell.WindowKey}}" class="sortlink{{if eq .Sort "traffic"}} on{{end}}" style="text-align:right;">TRAFFIC{{if eq .Sort "traffic"}} ▾{{end}}</a>
      <a href="?sort=error&win={{.Shell.WindowKey}}" class="sortlink{{if eq .Sort "error"}} on{{end}}" style="text-align:right;">ERROR %{{if eq .Sort "error"}} ▾{{end}}</a>
      <span style="text-align:right;">p50</span><span style="text-align:right;">p95</span>
      <a href="?sort=p99&win={{.Shell.WindowKey}}" class="sortlink{{if eq .Sort "p99"}} on{{end}}" style="text-align:right;">p99{{if eq .Sort "p99"}} ▾{{end}}</a>
      <span style="text-align:right;">AVG REQ</span><span style="text-align:right;">AVG RESP</span>
    </div>
    {{$svc := .Service}}
    {{$win := .Shell.WindowKey}}
    {{range .Endpoints}}
    <a class="trow" style="grid-template-columns:2.2fr 1fr 1fr .7fr .7fr .7fr .8fr .8fr;" href="/services/{{$svc}}/endpoint?method={{.Method}}&route={{.Route}}&win={{$win}}">
      <div style="display:flex;align-items:center;gap:11px;min-width:0;"><span class="chip {{mcls .Method}}">{{.Method}}</span><span style="font:500 13px var(--mono);white-space:nowrap;overflow:hidden;text-overflow:ellipsis;">{{.Route}}</span></div>
      <div class="tnum">{{rated .RatePerSec}}/s</div>
      <div class="tnum {{errc .ErrorRate}}">{{pctd .ErrorRate}}</div>
      <div class="tnum-s">{{msf .P50}}</div>
      <div class="tnum-s">{{msf .P95}}</div>
      <div class="tnum-s {{p99c .P99}}">{{msf .P99}}</div>
      <div class="tnum-s">{{bytes .ReqBytesAvg}}</div>
      <div class="tnum-s">{{bytes .RespBytesAvg}}</div>
    </a>
    {{else}}
    <div style="padding:22px 20px;color:var(--txt-3);font-size:13px;">No endpoints for this service in this window.</div>
    {{end}}
  </div>
  <div class="note">Route templates only — raw paths are never emitted (cardinality-safe)</div>
</div></div>
</main></div></body></html>{{end}}
`
