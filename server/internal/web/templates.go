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
	"bytes":    fmtBytes,
	"errc":     errClass,
	"p99c":     p99Class,
	"hc":       healthClass,
	"mcls":     methodClass,
	"codec":    codeClass,
	"barc":     classBarClass,
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
// never has to filter a var() out of a style attribute. The individual template
// groups live in their own files (templates_*.go) and are concatenated here.
const templatesHTML = tplChromeHTML + tplTablesHTML + tplDetailHTML + tplOnboardingHTML + tplPerformanceHTML

// tplChromeHTML holds the shared chrome: the head (fonts + the full CSS), the
// sidebar, the top-bar, and the reusable kpi card partial.
const tplChromeHTML = `
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
        {{if .KeyMask}}
        <span style="display:flex;align-items:center;gap:5px;font:600 10.5px var(--mono);color:var(--ok);">
          <span class="pulse" style="width:6px;height:6px;"><span style="background:var(--ok);"></span><span class="ping" style="background:var(--ok);"></span></span>LIVE
        </span>
        {{end}}
      </div>
      {{if .KeyMask}}
      <div style="font:500 11.5px var(--mono);color:var(--txt-2);letter-spacing:.3px;">mk_live_{{.KeyMask}}</div>
      {{else}}
      <a href="/setup" style="font:500 11.5px var(--mono);color:var(--txt-3);letter-spacing:.3px;text-decoration:none;">no active key</a>
      {{end}}
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
`
