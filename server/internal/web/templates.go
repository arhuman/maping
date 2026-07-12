package web

import "html/template"

// templateFuncs are the presentation helpers used by the templates. The new
// design helpers (msf/msv/msu/pctd/rated + the colour-class funcs) live in
// chrome.go; the legacy pct/ms/rate are kept for any remaining callers.
var templateFuncs = template.FuncMap{
	"pct":      func(f float64) string { return fmtFloat(f*100, 2) + "%" },
	"ms":       func(seconds float64) string { return fmtFloat(seconds*1000, 1) + " ms" },
	"rate":     func(r float64) string { return fmtFloat(r, 2) + "/s" },
	"msf":      fmtMsFull,
	"msv":      fmtMsVal,
	"msu":      fmtMsUnit,
	"pctd":     fmtPctD,
	"rated":    fmtRate,
	"errc":     errClass,
	"p99c":     p99Class,
	"hc":       healthClass,
	"mcls":     methodClass,
	"codec":    codeClass,
	"initials": initials,
}

// parseTemplates compiles every named template with the shared funcs. Called
// once per Handler; a parse error is a programming error surfaced at startup.
func parseTemplates() (*template.Template, error) {
	return template.New("dashboard").Funcs(templateFuncs).Parse(templatesHTML)
}

// templatesHTML defines the shared chrome (head/sidebar/topbar) plus the five
// dark-theme pages. It is fully server-rendered: no client JS, no CDN scripts;
// navigation, sort and window switching are plain links, and the detail charts
// are inline SVG (chart.go). Colours are emitted as CSS classes so html/template
// never has to filter a var() out of a style attribute.
const templatesHTML = `
{{define "head"}}
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <link rel="preconnect" href="https://fonts.googleapis.com">
  <link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
  <link href="https://fonts.googleapis.com/css2?family=Hanken+Grotesk:wght@400;500;600;700;800&family=JetBrains+Mono:wght@400;500;600;700&display=swap" rel="stylesheet">
  <style>
:root{
  --bg:#0A0C0F; --panel:#10141A; --panel-2:#141A22; --panel-3:#181F28;
  --line:rgba(255,255,255,.08); --line-soft:rgba(255,255,255,.045);
  --txt:#E8EDF3; --txt-2:#9AA4B2; --txt-3:#69727F;
  --ok:#5FE3A1; --warn:#F5C451; --err:#FF6B6B; --violet:#A78BFA; --blue:#5EA0FF;
  --accent:#B4F14A;
  --ui:'Hanken Grotesk',system-ui,sans-serif; --mono:'JetBrains Mono',ui-monospace,monospace;
}
*{box-sizing:border-box;}
html,body{height:100%;}
body{margin:0;background:var(--bg);color:var(--txt);font-family:var(--ui);-webkit-font-smoothing:antialiased;}
a{color:inherit;text-decoration:none;}
@keyframes mp-pulse{0%{transform:scale(.7);opacity:.85}70%{transform:scale(2.6);opacity:0}100%{opacity:0}}
@keyframes mp-fade{from{opacity:0;transform:translateY(10px)}to{opacity:1;transform:none}}
::-webkit-scrollbar{width:10px;height:10px}
::-webkit-scrollbar-thumb{background:rgba(255,255,255,.11);border-radius:8px}
::-webkit-scrollbar-track{background:transparent}
.app{display:grid;grid-template-columns:252px 1fr;height:100vh;width:100%;overflow:hidden;background:var(--bg);}
.main{display:flex;flex-direction:column;overflow:hidden;min-width:0;background:radial-gradient(1200px 600px at 78% -10%,rgba(180,241,74,.05),transparent 60%);}
.scrollbody{flex:1;overflow-y:auto;overflow-x:hidden;padding:28px 30px 60px;}
.mono{font-family:var(--mono);}
/* colour utilities */
.c-txt{color:var(--txt);} .c-txt2{color:var(--txt-2);} .c-txt3{color:var(--txt-3);}
.c-accent{color:var(--accent);} .c-ok{color:var(--ok);} .c-warn{color:var(--warn);}
.c-err{color:var(--err);} .c-violet{color:var(--violet);} .c-blue{color:var(--blue);}
/* method chips */
.chip{font:700 10px var(--mono);padding:3px 7px;border-radius:5px;letter-spacing:.5px;}
.m-get{color:var(--accent);background:rgba(180,241,74,.12);}
.m-post{color:var(--blue);background:rgba(94,160,255,.14);}
.m-delete{color:var(--err);background:rgba(255,107,107,.13);}
.m-put{color:var(--warn);background:rgba(245,196,81,.13);}
.m-patch{color:var(--violet);background:rgba(167,139,250,.14);}
.m-other{color:var(--txt-2);background:var(--panel-3);}
/* health dots */
.dot{width:9px;height:9px;border-radius:50%;display:inline-block;}
.dot-ok{background:var(--ok);box-shadow:0 0 0 3px rgba(95,227,161,.15);}
.dot-warn{background:var(--warn);box-shadow:0 0 0 3px rgba(245,196,81,.15);}
.dot-err{background:var(--err);box-shadow:0 0 0 3px rgba(255,107,107,.15);}
/* status-breakdown bars */
.bar-ok{background:var(--ok);} .bar-blue{background:var(--blue);} .bar-warn{background:var(--warn);}
.bar-err{background:var(--err);} .bar-txt3{background:var(--txt-3);}
/* cards */
.panel{background:var(--panel);border:1px solid var(--line);border-radius:14px;}
.kpi{background:var(--panel);border:1px solid var(--line);border-radius:13px;padding:16px 17px;}
.kpi-l{font:600 10.5px var(--mono);color:var(--txt-3);letter-spacing:.8px;}
.kpi-v{display:flex;align-items:baseline;gap:7px;margin-top:9px;}
.kpi-num{font:700 25px/1 var(--mono);letter-spacing:-1px;}
.kpi-u{font:600 12px var(--mono);color:var(--txt-3);}
.kpi-sub{font:500 11px var(--mono);color:var(--txt-3);margin-top:8px;}
.kpistrip{display:grid;gap:14px;margin-bottom:24px;}
/* table */
.thead,.trow{display:grid;align-items:center;}
.thead{padding:12px 20px;border-bottom:1px solid var(--line);font:600 10.5px var(--mono);color:var(--txt-3);letter-spacing:.8px;background:var(--panel-2);}
.trow{padding:13px 20px;border-bottom:1px solid var(--line-soft);cursor:pointer;transition:background .12s;color:inherit;}
.trow:hover{background:var(--panel-2);}
.tnum{text-align:right;font:600 13px var(--mono);}
.tnum-s{text-align:right;font:500 12.5px var(--mono);color:var(--txt-2);}
.sortlink{color:var(--txt-3);}
.sortlink.on{color:var(--accent);}
.note{font:500 11px var(--mono);color:var(--txt-3);margin-top:12px;text-align:right;}
.warnbar{display:flex;align-items:center;gap:11px;background:rgba(245,196,81,.08);border:1px solid rgba(245,196,81,.28);color:var(--warn);border-radius:11px;padding:12px 15px;margin-bottom:20px;font-size:12.5px;}
/* sidebar */
.aside{border-right:1px solid var(--line);background:linear-gradient(180deg,#0C0F14,#0A0C0F);display:flex;flex-direction:column;padding:22px 16px;gap:26px;overflow:hidden;}
.nav-item{display:flex;align-items:center;gap:11px;width:100%;padding:9px 10px;border-radius:9px;font:600 13px var(--ui);color:var(--txt-2);}
.nav-item:hover{background:rgba(255,255,255,.03);}
.nav-item.on{background:rgba(180,241,74,.10);color:var(--txt);}
.nav-ico{width:18px;height:18px;display:grid;place-items:center;color:var(--txt-3);}
.nav-item.on .nav-ico{color:var(--accent);}
.nav-badge{font:600 10px var(--mono);color:var(--txt-2);background:var(--panel-3);padding:2px 6px;border-radius:20px;}
/* topbar */
.header{display:flex;align-items:center;gap:16px;padding:16px 30px;border-bottom:1px solid var(--line);background:rgba(10,12,15,.6);backdrop-filter:blur(8px);}
.crumbs{display:flex;align-items:center;gap:9px;font:600 12.5px var(--mono);color:var(--txt-3);}
.wbtn{border:none;cursor:pointer;font:600 12px var(--mono);padding:5px 11px;border-radius:6px;background:transparent;color:var(--txt-2);}
.wbtn.on{background:var(--accent);color:#0A0C0F;}
.livelink{display:flex;align-items:center;gap:7px;font:600 11.5px var(--mono);color:var(--txt-3);}
.livelink.on{color:var(--txt-2);}
.pulse{position:relative;display:inline-block;}
.pulse span{position:absolute;inset:0;border-radius:50%;}
.pulse .ping{animation:mp-pulse 2.4s ease-out infinite;}
.fade{animation:mp-fade .4s ease both;}
pre{margin:0;padding:16px 17px;font:500 12.5px/1.7 var(--mono);color:var(--txt-2);overflow-x:auto;}
  </style>
{{end}}

{{define "sidebar"}}
<aside class="aside">
  <div style="display:flex;align-items:center;gap:11px;padding:0 6px;">
    <div style="width:34px;height:34px;display:grid;place-items:center;background:var(--panel-2);border:1px solid var(--line);border-radius:9px;">
      <svg width="19" height="19" viewBox="0 0 24 24" fill="none"><path d="M2 15 L6 15 L8 6 L11 20 L14 3 L16 15 L22 15" stroke="#B4F14A" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" fill="none"></path></svg>
    </div>
    <div style="line-height:1;">
      <div style="font-weight:800;font-size:17px;letter-spacing:-.3px;">m<span class="c-accent">API</span>ng</div>
      <div style="font:500 9.5px/1 var(--mono);color:var(--txt-3);letter-spacing:1.5px;margin-top:3px;">MONITORED&nbsp;API · NG</div>
    </div>
  </div>
  <div style="display:flex;align-items:center;gap:10px;width:100%;padding:9px 11px;background:var(--panel);border:1px solid var(--line);border-radius:10px;">
    <div style="width:26px;height:26px;border-radius:7px;background:linear-gradient(135deg,var(--accent),#6fae2e);display:grid;place-items:center;font:800 12px var(--ui);color:#0A0C0F;">{{initials .Org}}</div>
    <div style="flex:1;line-height:1.2;overflow:hidden;">
      <div style="font-size:12.5px;font-weight:600;white-space:nowrap;">{{.Org}}</div>
      <div style="font:500 10px/1 var(--mono);color:var(--txt-3);margin-top:2px;">organization</div>
    </div>
  </div>
  <nav style="display:flex;flex-direction:column;gap:3px;">
    <div style="font:600 10px/1 var(--mono);color:var(--txt-3);letter-spacing:1.4px;padding:0 8px 8px;">MONITOR</div>
    {{range .Nav}}
      <a href="{{.Href}}" class="nav-item{{if .Active}} on{{end}}">
        <span class="nav-ico">{{.Icon}}</span><span style="flex:1;">{{.Label}}</span>
        {{if .Badge}}<span class="nav-badge">{{.Badge}}</span>{{end}}
      </a>
    {{end}}
  </nav>
  <div style="margin-top:auto;display:flex;flex-direction:column;gap:12px;">
    <div style="background:var(--panel);border:1px solid var(--line);border-radius:11px;padding:12px 13px;">
      <div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:9px;">
        <span style="font:600 10px var(--mono);color:var(--txt-3);letter-spacing:1px;">INGEST KEY</span>
        <span style="display:flex;align-items:center;gap:5px;font:600 10.5px var(--mono);color:var(--ok);">
          <span class="pulse" style="width:6px;height:6px;"><span style="background:var(--ok);"></span><span class="ping" style="background:var(--ok);"></span></span>LIVE
        </span>
      </div>
      <div style="font:500 11.5px var(--mono);color:var(--txt-2);letter-spacing:.3px;">mk_live_····a91f</div>
    </div>
    <div style="display:flex;align-items:center;gap:8px;padding:0 4px;">
      <div style="width:30px;height:30px;border-radius:50%;background:var(--panel-3);border:1px solid var(--line);display:grid;place-items:center;font:700 11px var(--ui);color:var(--txt-2);">{{initials .User}}</div>
      <div style="line-height:1.2;flex:1;overflow:hidden;">
        <div style="font-size:12px;font-weight:600;white-space:nowrap;">{{.User}}</div>
        <div style="font:500 10px/1 var(--mono);color:var(--txt-3);margin-top:2px;">{{.Role}}</div>
      </div>
    </div>
  </div>
</aside>
{{end}}

{{define "topbar"}}
<header class="header">
  <div style="flex:1;min-width:0;">
    <div class="crumbs">
      {{range $i, $c := .Crumbs}}
        {{if $i}}<span style="color:var(--txt-3);opacity:.5;">/</span>{{end}}
        {{if $c.Href}}<a href="{{$c.Href}}" class="c-accent">{{$c.Label}}</a>{{else}}<span class="c-txt2">{{$c.Label}}</span>{{end}}
      {{end}}
    </div>
    <h1 style="margin:6px 0 0;font-size:21px;font-weight:800;letter-spacing:-.4px;">{{.PageTitle}}</h1>
  </div>
  {{if .ShowControls}}
  <div style="display:flex;align-items:center;gap:14px;">
    {{if .LiveHref}}
    <a href="{{.LiveHref}}" class="livelink{{if .Live}} on{{end}}">
      {{if .Live}}<span class="pulse" style="width:7px;height:7px;"><span style="background:var(--accent);"></span><span class="ping" style="background:var(--accent);"></span></span>live · {{.FlushLabel}}{{else}}<span style="width:7px;height:7px;border-radius:50%;background:var(--txt-3);display:inline-block;"></span>paused · go live{{end}}
    </a>
    {{else}}
    <div style="display:flex;align-items:center;gap:7px;font:600 11.5px var(--mono);color:var(--txt-2);">
      <span class="pulse" style="width:7px;height:7px;"><span style="background:var(--accent);"></span><span class="ping" style="background:var(--accent);"></span></span>live · {{.FlushLabel}}
    </div>
    {{end}}
    <div style="display:flex;background:var(--panel);border:1px solid var(--line);border-radius:9px;padding:3px;">
      {{range .Windows}}<a href="{{.Href}}" class="wbtn{{if .Active}} on{{end}}">{{.Key}}</a>{{end}}
    </div>
  </div>
  {{end}}
</header>
{{end}}

{{define "kpi"}}
<div class="kpi">
  <div class="kpi-l">{{.Label}}</div>
  <div class="kpi-v"><span class="kpi-num {{.ColorClass}}">{{.Value}}</span>{{if .Unit}}<span class="kpi-u">{{.Unit}}</span>{{end}}</div>
  {{if .Sub}}<div class="kpi-sub">{{.Sub}}</div>{{end}}
</div>
{{end}}

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
    <div class="thead" style="grid-template-columns:2.4fr 1fr 1fr .8fr .8fr .8fr;">
      <span>ENDPOINT</span>
      <a href="?sort=traffic&win={{.Shell.WindowKey}}" class="sortlink{{if eq .Sort "traffic"}} on{{end}}" style="text-align:right;">TRAFFIC{{if eq .Sort "traffic"}} ▾{{end}}</a>
      <a href="?sort=error&win={{.Shell.WindowKey}}" class="sortlink{{if eq .Sort "error"}} on{{end}}" style="text-align:right;">ERROR %{{if eq .Sort "error"}} ▾{{end}}</a>
      <span style="text-align:right;">p50</span><span style="text-align:right;">p95</span>
      <a href="?sort=p99&win={{.Shell.WindowKey}}" class="sortlink{{if eq .Sort "p99"}} on{{end}}" style="text-align:right;">p99{{if eq .Sort "p99"}} ▾{{end}}</a>
    </div>
    {{$svc := .Service}}
    {{range .Endpoints}}
    <a class="trow" style="grid-template-columns:2.4fr 1fr 1fr .8fr .8fr .8fr;" href="/services/{{$svc}}/endpoint?method={{.Method}}&route={{.Route}}">
      <div style="display:flex;align-items:center;gap:11px;min-width:0;"><span class="chip {{mcls .Method}}">{{.Method}}</span><span style="font:500 13px var(--mono);white-space:nowrap;overflow:hidden;text-overflow:ellipsis;">{{.Route}}</span></div>
      <div class="tnum">{{rated .RatePerSec}}/s</div>
      <div class="tnum {{errc .ErrorRate}}">{{pctd .ErrorRate}}</div>
      <div class="tnum-s">{{msf .P50}}</div>
      <div class="tnum-s">{{msf .P95}}</div>
      <div class="tnum-s {{p99c .P99}}">{{msf .P99}}</div>
    </a>
    {{else}}
    <div style="padding:22px 20px;color:var(--txt-3);font-size:13px;">No endpoints for this service in this window.</div>
    {{end}}
  </div>
  <div class="note">Route templates only — raw paths are never emitted (cardinality-safe)</div>
</div></div>
</main></div></body></html>{{end}}

{{define "detail"}}<!doctype html>
<html lang="en"><head>{{template "head"}}<title>mAPI-ng — {{.Method}} {{.Route}}</title></head>
<body><div class="app">{{template "sidebar" .Shell}}<main class="main">{{template "topbar" .Shell}}
<div class="scrollbody"><div class="fade">
  <div style="display:flex;align-items:center;gap:12px;margin-bottom:20px;"><span class="chip {{mcls .Method}}" style="font-size:12px;padding:5px 10px;border-radius:7px;">{{.Method}}</span><span style="font:600 18px var(--mono);">{{.Route}}</span></div>
  <div class="panel" style="padding:13px 16px;margin-bottom:18px;display:flex;align-items:center;gap:14px;flex-wrap:wrap;">
    <span style="font:700 10px var(--mono);color:var(--txt-3);letter-spacing:1px;flex-shrink:0;">DEBUG CONTEXT</span>
    <span id="mp-debug" class="mono" style="flex:1;min-width:220px;font-size:12px;color:var(--txt-2);word-break:break-word;">{{.Debug.Summary}}</span>
    <button type="button" data-copy="mp-debug" style="flex-shrink:0;padding:5px 11px;border:1px solid var(--line);border-radius:7px;background:var(--panel-3);color:var(--txt-2);font:600 11px var(--mono);cursor:pointer;">⧉ copy</button>
  </div>
  <div class="kpistrip" style="grid-template-columns:repeat(5,1fr);">{{range .Stats}}{{template "kpi" .}}{{end}}</div>
  <div style="display:grid;grid-template-columns:1.5fr 1fr;gap:18px;align-items:start;">
    <div class="panel" style="padding:18px 20px;">
      <div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:14px;"><span style="font-size:13.5px;font-weight:700;">Rate &amp; latency over time</span><div style="display:flex;gap:14px;"><span style="display:flex;align-items:center;gap:6px;font:600 11px var(--mono);color:var(--txt-2);"><span style="width:12px;height:3px;border-radius:2px;background:var(--accent);"></span>rate</span><span style="display:flex;align-items:center;gap:6px;font:600 11px var(--mono);color:var(--txt-2);"><span style="width:12px;height:3px;border-radius:2px;background:var(--violet);"></span>p95</span></div></div>
      {{.TSChart}}
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
      <div style="margin-top:18px;padding-top:16px;border-top:1px solid var(--line);">
        <div style="font:600 10px var(--mono);color:var(--txt-3);letter-spacing:.8px;margin-bottom:10px;">EXACT CODES</div>
        <div style="display:flex;flex-wrap:wrap;gap:8px;">
          {{range .Detail.Codes}}<span style="font:600 11px var(--mono);color:var(--txt-2);background:var(--panel-3);border:1px solid var(--line);padding:4px 9px;border-radius:7px;"><span class="{{codec .Code}}">{{.Code}}</span> · {{.Count}}</span>{{end}}
        </div>
      </div>
    </div>
  </div>
  <div class="panel" style="padding:18px 20px;margin-top:18px;">
    <div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:6px;"><span style="font-size:13.5px;font-weight:700;">Latency distribution</span><span style="font:500 11px var(--mono);color:var(--txt-3);">DDSketch · γ=1.01 · ~1% relative error</span></div>
    <div style="font:500 11px var(--mono);color:var(--txt-3);margin-bottom:8px;">merged bucket counts · log-spaced latency</div>
    {{.HistChart}}
  </div>
</div></div>
</main></div>
<script src="/assets/copy.js" defer></script>
</body></html>{{end}}

{{define "onboarding"}}<!doctype html>
<html lang="en"><head>{{template "head"}}{{if .Refresh}}<meta http-equiv="refresh" content="3">{{end}}<title>mAPI-ng — get started</title></head>
<body><div class="app">{{template "sidebar" .Shell}}<main class="main">{{template "topbar" .Shell}}
<div class="scrollbody"><div style="max-width:1060px;margin:0 auto;" class="fade">
  {{if .Frozen}}<div class="warnbar"><span style="font-size:15px;">⚠</span><span class="c-txt2"><b class="c-warn">Cardinality frozen.</b> New series are rejected for this tenant.</span></div>{{end}}
  <div style="display:inline-flex;align-items:center;gap:8px;font:600 11px var(--mono);color:var(--accent);background:rgba(180,241,74,.09);border:1px solid rgba(180,241,74,.22);padding:6px 12px;border-radius:20px;letter-spacing:.5px;">◗ ZERO-CONFIG SETUP</div>
  <h2 style="font-size:38px;font-weight:800;letter-spacing:-1px;margin:20px 0 10px;max-width:640px;line-height:1.05;">One env var. Live in <span class="c-accent">seconds</span>.</h2>
  <p style="font-size:15.5px;color:var(--txt-2);max-width:560px;margin:0 0 30px;line-height:1.6;">No Prometheus. No Grafana. No YAML. Add the middleware, set <span style="font:600 13px var(--mono);color:var(--txt);">MAPING_KEY</span>, and RED metrics for every Go endpoint start flowing.</p>
  <div style="display:grid;grid-template-columns:1.15fr .85fr;gap:22px;align-items:start;min-width:0;">
    <div style="display:flex;flex-direction:column;gap:16px;min-width:0;">
      <div class="panel" style="overflow:hidden;">
        <div style="display:flex;align-items:center;gap:8px;padding:11px 15px;border-bottom:1px solid var(--line);background:var(--panel-2);"><span style="font:700 11px var(--mono);color:var(--accent);">1</span><span style="font-size:12.5px;font-weight:600;">Wire the middleware</span><span style="margin-left:auto;font:500 10.5px var(--mono);color:var(--txt-3);">main.go</span></div>
        <pre>rec := maping.NewRecorder(
    maping.WithService("checkout-api"),
)
r.Use(mapinggin.MiddlewareWithRecorder(rec)) // above Recovery
r.Use(gin.Recovery())</pre>
      </div>
      <div class="panel" style="overflow:hidden;">
        <div style="display:flex;align-items:center;gap:8px;padding:11px 15px;border-bottom:1px solid var(--line);background:var(--panel-2);"><span style="font:700 11px var(--mono);color:var(--accent);">2</span><span style="font-size:12.5px;font-weight:600;">Activate</span></div>
        <pre style="color:var(--accent);">export MAPING_KEY=mk_live_9f2c····a91f</pre>
      </div>
      <div style="display:flex;align-items:center;gap:10px;font-size:12.5px;color:var(--txt-3);padding:2px 4px;"><span class="c-txt2">◇</span> Absent key ⇒ the middleware is a no-op. Shipping mAPI-ng is always safe.</div>
    </div>
    <div class="panel" style="padding:20px;">
      <div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:6px;"><span style="font:700 11px var(--mono);color:var(--txt-3);letter-spacing:1px;">HANDSHAKE</span></div>
      <div style="font-size:12px;color:var(--txt-3);margin-bottom:18px;">Live status from your first recorder.</div>
      <div style="display:flex;flex-direction:column;gap:2px;">
        {{range .Steps}}
        <div style="display:flex;align-items:flex-start;gap:13px;padding:11px 4px;">
          <div style="display:flex;flex-direction:column;align-items:center;">
            <div style="width:22px;height:22px;border-radius:50%;display:grid;place-items:center;font:700 11px var(--mono);
              {{if eq .DotClass "dot-done"}}background:var(--accent);border:1.5px solid var(--accent);color:#0A0C0F;
              {{else if eq .DotClass "dot-current"}}background:rgba(180,241,74,.12);border:1.5px solid var(--accent);color:var(--accent);
              {{else}}background:var(--panel-3);border:1.5px solid var(--line);color:var(--txt-3);{{end}}">{{.Icon}}</div>
            {{if .Connector}}<div style="width:1.5px;height:22px;background:var(--line);margin-top:2px;"></div>{{end}}
          </div>
          <div style="padding-top:1px;"><div style="font-size:13px;font-weight:600;" class="{{.LabelClass}}">{{.Label}}</div><div style="font:500 11px var(--mono);color:var(--txt-3);margin-top:2px;">{{.Sub}}</div></div>
        </div>
        {{end}}
      </div>
      {{if .Connected}}
      <div style="margin-top:16px;padding-top:14px;border-top:1px solid var(--line);">
        <div style="font:600 10px var(--mono);color:var(--txt-3);letter-spacing:.8px;margin-bottom:8px;">CONNECTED</div>
        {{range .Connected}}<div style="font:500 11px var(--mono);color:var(--txt-2);margin-bottom:4px;">{{.Service}}{{if .Instance}} · {{.Instance}}{{end}}</div>{{end}}
      </div>
      {{end}}
    </div>
  </div>
</div></div>
</main></div></body></html>{{end}}

{{define "setup"}}<!doctype html>
<html lang="en"><head>{{template "head"}}{{if .Refresh}}<meta http-equiv="refresh" content="3">{{end}}<title>mAPI-ng — setup</title></head>
<body><div class="app">{{template "sidebar" .Shell}}<main class="main">{{template "topbar" .Shell}}
<div class="scrollbody"><div style="max-width:1060px;margin:0 auto;" class="fade">
  {{if .Frozen}}<div class="warnbar"><span style="font-size:15px;">⚠</span><span class="c-txt2"><b class="c-warn">Cardinality frozen.</b> New series are rejected for this tenant.</span></div>{{end}}
  {{if .NewToken}}
  <div class="panel" style="padding:20px 22px;margin-bottom:22px;border-color:rgba(180,241,74,.3);">
    <div style="display:flex;align-items:center;gap:8px;margin-bottom:12px;"><span style="font:600 12px var(--mono);color:var(--warn);">⚠ COPY IT NOW — YOU WON'T SEE IT AGAIN</span><button type="button" data-copy="mp-newkey" style="margin-left:auto;padding:5px 10px;border:1px solid var(--line);border-radius:7px;background:var(--panel-3);color:var(--txt-2);font:600 11px var(--mono);cursor:pointer;">⧉ copy</button></div>
    <code id="mp-newkey" style="user-select:all;display:block;padding:14px 15px;background:var(--bg);border:1px solid rgba(180,241,74,.3);border-radius:10px;color:var(--accent);font:600 12.5px/1.5 var(--mono);word-break:break-all;">export MAPING_KEY={{.NewToken}}</code>
  </div>
  {{end}}
  <div style="display:grid;grid-template-columns:1.1fr .9fr;gap:22px;align-items:start;min-width:0;">
    <div style="display:flex;flex-direction:column;gap:16px;min-width:0;">
      {{if .ShowKeys}}
      <div class="panel" style="overflow:hidden;">
        <div class="thead" style="grid-template-columns:1.4fr 1.1fr .9fr .7fr;"><span>LABEL</span><span>KEY</span><span>CREATED</span><span style="text-align:right;">ACTION</span></div>
        {{range .Keys}}
        <div class="trow" style="grid-template-columns:1.4fr 1.1fr .9fr .7fr;cursor:default;">
          <div style="font-size:13px;font-weight:600;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;">{{.Label}}</div>
          <div class="mono" style="color:var(--txt-2);font-size:12.5px;">mk_live_{{.Masked}}</div>
          <div class="mono" style="color:var(--txt-3);font-size:12px;">{{.Created}}</div>
          <div style="text-align:right;">
            {{if .Revoked}}<span class="mono" style="color:var(--txt-3);font-size:11px;">revoked</span>
            {{else}}<form method="post" action="/setup/keys/{{.ID}}/revoke" style="display:inline;"><input type="hidden" name="csrf_token" value="{{$.CSRFToken}}"><button type="submit" style="padding:4px 10px;border:1px solid rgba(255,107,107,.3);border-radius:7px;background:rgba(255,107,107,.08);color:var(--err);font:600 11px var(--mono);cursor:pointer;">revoke</button></form>{{end}}
          </div>
        </div>
        {{else}}
        <div style="padding:22px 20px;color:var(--txt-3);font-size:13px;">No keys yet. Create one to start ingesting.</div>
        {{end}}
      </div>
      <form method="post" action="/setup/keys" class="panel" style="padding:15px 17px;display:flex;gap:10px;align-items:center;">
        <input type="hidden" name="csrf_token" value="{{.CSRFToken}}">
        <input name="label" placeholder="label (e.g. checkout-api)" maxlength="64" autocomplete="off" style="flex:1;padding:9px 12px;background:var(--bg);border:1px solid var(--line);border-radius:9px;color:var(--txt);font:500 12.5px var(--mono);outline:none;">
        <button type="submit" style="padding:9px 16px;border:none;border-radius:9px;background:var(--accent);color:#0A0C0F;font:700 12.5px var(--ui);cursor:pointer;">Create key</button>
      </form>
      <div style="font:500 11px var(--mono);color:var(--txt-3);padding:2px 4px;line-height:1.5;">The full token carries this deployment's collector endpoint, so <span class="c-txt2">MAPING_KEY</span> is the only env var your service needs. The secret is shown once at creation.</div>
      {{else}}
      <div class="panel" style="padding:20px 22px;color:var(--txt-3);font-size:13px;line-height:1.6;">Self-serve key management is available once the control plane is configured. This deployment authenticates ingest with a static <span class="mono c-txt2">dev-key</span>.</div>
      {{end}}
    </div>
    <div class="panel" style="padding:20px;">
      <div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:6px;"><span style="font:700 11px var(--mono);color:var(--txt-3);letter-spacing:1px;">HANDSHAKE</span></div>
      <div style="font-size:12px;color:var(--txt-3);margin-bottom:18px;">Live status from your first recorder.</div>
      <div style="display:flex;flex-direction:column;gap:2px;">
        {{range .Steps}}
        <div style="display:flex;align-items:flex-start;gap:13px;padding:11px 4px;">
          <div style="display:flex;flex-direction:column;align-items:center;">
            <div style="width:22px;height:22px;border-radius:50%;display:grid;place-items:center;font:700 11px var(--mono);
              {{if eq .DotClass "dot-done"}}background:var(--accent);border:1.5px solid var(--accent);color:#0A0C0F;
              {{else if eq .DotClass "dot-current"}}background:rgba(180,241,74,.12);border:1.5px solid var(--accent);color:var(--accent);
              {{else}}background:var(--panel-3);border:1.5px solid var(--line);color:var(--txt-3);{{end}}">{{.Icon}}</div>
            {{if .Connector}}<div style="width:1.5px;height:22px;background:var(--line);margin-top:2px;"></div>{{end}}
          </div>
          <div style="padding-top:1px;"><div style="font-size:13px;font-weight:600;" class="{{.LabelClass}}">{{.Label}}</div><div style="font:500 11px var(--mono);color:var(--txt-3);margin-top:2px;">{{.Sub}}</div></div>
        </div>
        {{end}}
      </div>
      {{if .Connected}}
      <div style="margin-top:16px;padding-top:14px;border-top:1px solid var(--line);">
        <div style="font:600 10px var(--mono);color:var(--txt-3);letter-spacing:.8px;margin-bottom:8px;">CONNECTED</div>
        {{range .Connected}}<div style="font:500 11px var(--mono);color:var(--txt-2);margin-bottom:4px;">{{.Service}}{{if .Instance}} · {{.Instance}}{{end}}</div>{{end}}
      </div>
      {{end}}
    </div>
  </div>
</div></div>
</main></div>
<script src="/assets/copy.js" defer></script>
</body></html>{{end}}

{{define "performance"}}<!doctype html>
<html lang="en"><head>{{template "head"}}<title>mAPI-ng — performance</title></head>
<body><div class="app">{{template "sidebar" .Shell}}<main class="main">{{template "topbar" .Shell}}
<div class="scrollbody"><div style="max-width:1080px;" class="fade">
  <p style="font-size:14px;color:var(--txt-2);max-width:620px;margin:0 0 26px;line-height:1.6;">mAPI-ng aggregates in-process into DDSketch summaries before shipping — so one small record can represent thousands of requests. More data per second ingested, far less disk used, faster queries than raw-event pipelines.</p>
  <div class="kpistrip" style="grid-template-columns:repeat(4,1fr);">
    <div class="kpi"><div class="kpi-l">TIME TO FIRST DATA</div><div class="kpi-v"><span class="kpi-num c-accent" style="font-size:30px;">3.2</span><span class="kpi-u">s</span></div><div class="kpi-sub" style="color:var(--txt-3);">env var → live RED metrics, cold start</div></div>
    <div class="kpi"><div class="kpi-l">INGEST THROUGHPUT</div><div class="kpi-v"><span class="kpi-num c-txt" style="font-size:30px;">182</span><span class="kpi-u">k req/s</span></div><div class="kpi-sub">represented by 41 summaries/s per node</div></div>
    <div class="kpi"><div class="kpi-l">COMPRESSION</div><div class="kpi-v"><span class="kpi-num c-accent" style="font-size:30px;">4.4k</span><span class="kpi-u">×</span></div><div class="kpi-sub">requests per shipped summary</div></div>
    <div class="kpi"><div class="kpi-l">DASHBOARD QUERY</div><div class="kpi-v"><span class="kpi-num c-txt" style="font-size:30px;">34</span><span class="kpi-u">ms</span></div><div class="kpi-sub">p99 aggregate over 1h window</div></div>
  </div>
  <div style="display:grid;grid-template-columns:1fr 1fr;gap:18px;">
    <div class="panel" style="padding:20px 22px;">
      <div style="font-size:14px;font-weight:700;margin-bottom:4px;">Disk per day</div>
      <div style="font:500 11px var(--mono);color:var(--txt-3);margin-bottom:20px;">same traffic · 4 services · 24h</div>
      <div style="display:flex;flex-direction:column;gap:18px;">
        <div><div style="display:flex;justify-content:space-between;margin-bottom:7px;font:600 12px var(--mono);"><span class="c-txt2">Raw-event pipeline</span><span class="c-err">47.2 GB</span></div><div style="height:12px;background:var(--panel-3);border-radius:6px;overflow:hidden;"><div style="height:100%;width:100%;background:linear-gradient(90deg,#FF6B6B,#c94a4a);border-radius:6px;"></div></div></div>
        <div><div style="display:flex;justify-content:space-between;margin-bottom:7px;font:600 12px var(--mono);"><span class="c-txt2">mAPI-ng summaries</span><span class="c-accent">0.61 GB</span></div><div style="height:12px;background:var(--panel-3);border-radius:6px;overflow:hidden;"><div style="height:100%;width:1.3%;min-width:5px;background:var(--accent);border-radius:6px;"></div></div></div>
      </div>
      <div style="margin-top:20px;padding-top:16px;border-top:1px solid var(--line);display:flex;align-items:baseline;gap:8px;"><span style="font:700 28px var(--mono);color:var(--accent);letter-spacing:-1px;">77×</span><span style="font-size:12.5px;color:var(--txt-2);">less disk — error counts derived at query time, never stored.</span></div>
    </div>
    <div class="panel" style="padding:20px 22px;">
      <div style="font-size:14px;font-weight:700;margin-bottom:4px;">Rollup tiers</div>
      <div style="font:500 11px var(--mono);color:var(--txt-3);margin-bottom:20px;">counters sum · sketches merge bucket-wise (exact)</div>
      <div style="display:flex;align-items:center;gap:14px;margin-bottom:14px;"><div style="width:56px;font:700 14px var(--mono);color:var(--accent);">10s</div><div style="flex:1;height:8px;background:var(--panel-3);border-radius:5px;overflow:hidden;"><div style="height:100%;width:100%;background:var(--accent);border-radius:5px;"></div></div><div style="width:96px;text-align:right;font:500 11px var(--mono);color:var(--txt-3);">raw · 6h</div></div>
      <div style="display:flex;align-items:center;gap:14px;margin-bottom:14px;"><div style="width:56px;font:700 14px var(--mono);color:var(--accent);">1min</div><div style="flex:1;height:8px;background:var(--panel-3);border-radius:5px;overflow:hidden;"><div style="height:100%;width:62%;background:var(--accent);border-radius:5px;"></div></div><div style="width:96px;text-align:right;font:500 11px var(--mono);color:var(--txt-3);">30 days</div></div>
      <div style="display:flex;align-items:center;gap:14px;margin-bottom:14px;"><div style="width:56px;font:700 14px var(--mono);color:var(--accent);">1hr</div><div style="flex:1;height:8px;background:var(--panel-3);border-radius:5px;overflow:hidden;"><div style="height:100%;width:34%;background:var(--accent);border-radius:5px;"></div></div><div style="width:96px;text-align:right;font:500 11px var(--mono);color:var(--txt-3);">13 months</div></div>
      <div style="display:flex;align-items:center;gap:14px;margin-bottom:14px;"><div style="width:56px;font:700 14px var(--mono);color:var(--accent);">1day</div><div style="flex:1;height:8px;background:var(--panel-3);border-radius:5px;overflow:hidden;"><div style="height:100%;width:16%;background:var(--accent);border-radius:5px;"></div></div><div style="width:96px;text-align:right;font:500 11px var(--mono);color:var(--txt-3);">forever</div></div>
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
