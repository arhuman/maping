package docs

import (
	"bytes"
	"html/template"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	gmhtml "github.com/yuin/goldmark/renderer/html"
)

// md is the shared Markdown converter for every doc page. GFM adds tables,
// strikethrough, task lists and autolinks; WithAutoHeadingID gives each heading a
// slug so in-page anchors work. The renderer stays in its SAFE default (raw HTML
// in a source file is escaped, not passed through) — the content is build-time
// trusted (embedded), but keeping the safe default means a future externally
// sourced page cannot inject markup.
var md = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	goldmark.WithRendererOptions(gmhtml.WithHardWraps()),
)

// MarkdownToHTML converts a Markdown source to the HTML fragment a doc page drops
// into the prose column. It is exported so a composing build renders its own
// embedded Markdown through the same converter before handing the result to
// RouteContext.RenderDoc, keeping every doc page — core or extension — identical.
func MarkdownToHTML(src []byte) (template.HTML, error) {
	var buf bytes.Buffer
	if err := md.Convert(src, &buf); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil //nolint:gosec // build-time trusted content, safe renderer
}

// shellData is the doc-page view model: the merged, grouped table of contents (the
// left rail, identical on every page) plus the current page's title and rendered
// body. Groups are pre-sorted so the template is pure iteration.
type shellData struct {
	Title  string
	Body   template.HTML
	Groups []navGroup
}

// navGroup is one heading in the left rail (e.g. "Product", "Enterprise") and its
// ordered links; navLink carries the active flag for the current page.
type navGroup struct {
	Group string
	Items []navLink
}

type navLink struct {
	Title, Href string
	Active      bool
}

// shellTmpl is the standalone public doc shell — its own chrome (not the
// authenticated dashboard sidebar), so /doc renders for anonymous visitors in the
// community build and behind the marketing site alike. All CSS is inline in the
// <style> block (no external stylesheet, matching the dashboard) and there is no
// JavaScript, so the page satisfies a script-src 'none' CSP.
var shellTmpl = template.Must(template.New("doc").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} · mAPI-ng docs</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Hanken+Grotesk:wght@400;500;600;700;800&family=JetBrains+Mono:wght@400;500;600;700&display=swap" rel="stylesheet">
<style>
:root{
  --bg:#0A0C0F; --panel:#10141A; --panel-2:#141A22; --panel-3:#181F28;
  --line:rgba(255,255,255,.08); --line-soft:rgba(255,255,255,.045);
  --txt:#E8EDF3; --txt-2:#9AA4B2; --txt-3:#69727F;
  --accent:#B4F14A; --blue:#5EA0FF;
  --ui:'Hanken Grotesk',system-ui,sans-serif; --mono:'JetBrains Mono',ui-monospace,monospace;
}
*{box-sizing:border-box;}
body{margin:0;background:var(--bg);color:var(--txt);font-family:var(--ui);-webkit-font-smoothing:antialiased;}
a{color:var(--accent);text-decoration:none;}
a:hover{text-decoration:underline;}
.wrap{display:grid;grid-template-columns:264px minmax(0,1fr);min-height:100vh;}
.rail{border-right:1px solid var(--line);background:linear-gradient(180deg,#0C0F14,#0A0C0F);padding:26px 18px;position:sticky;top:0;align-self:start;height:100vh;overflow-y:auto;}
.brand{display:flex;align-items:baseline;gap:8px;font:800 17px var(--ui);color:var(--txt);margin-bottom:4px;}
.brand span{font:600 11px var(--mono);color:var(--accent);letter-spacing:.5px;}
.brand:hover{text-decoration:none;}
.tagline{font:500 11px var(--mono);color:var(--txt-3);margin:0 0 24px;letter-spacing:.3px;}
.grp{font:700 10px var(--mono);color:var(--txt-3);letter-spacing:1px;text-transform:uppercase;margin:22px 6px 8px;}
.lnk{display:block;padding:7px 10px;border-radius:8px;font:600 13px var(--ui);color:var(--txt-2);}
.lnk:hover{background:var(--panel-2);color:var(--txt);text-decoration:none;}
.lnk.on{background:rgba(180,241,74,.10);color:var(--txt);}
.main{padding:52px 0;display:flex;justify-content:center;}
.prose{width:100%;max-width:760px;padding:0 40px;}
.prose h1{font:800 32px/1.15 var(--ui);letter-spacing:-.5px;margin:0 0 24px;}
.prose h2{font:700 22px/1.2 var(--ui);letter-spacing:-.3px;margin:44px 0 14px;padding-top:14px;border-top:1px solid var(--line-soft);}
.prose h3{font:700 16px var(--ui);margin:28px 0 10px;}
.prose p{font:400 15px/1.7 var(--ui);color:var(--txt);margin:0 0 16px;}
.prose li{font:400 15px/1.7 var(--ui);color:var(--txt);margin:6px 0;}
.prose ul,.prose ol{padding-left:22px;margin:0 0 16px;}
.prose a{color:var(--blue);}
.prose strong{color:var(--txt);font-weight:700;}
.prose code{font:500 13px var(--mono);background:var(--panel-3);color:#D8E4C0;padding:2px 6px;border-radius:5px;}
.prose pre{background:var(--panel);border:1px solid var(--line);border-radius:12px;padding:16px 18px;overflow-x:auto;margin:0 0 18px;}
.prose pre code{background:none;padding:0;color:var(--txt);font-size:13px;line-height:1.6;}
.prose blockquote{margin:0 0 18px;padding:12px 18px;border-left:3px solid var(--accent);background:var(--panel-2);border-radius:0 10px 10px 0;color:var(--txt-2);}
.prose blockquote p{margin:0;font-size:14px;}
.prose table{width:100%;border-collapse:collapse;margin:0 0 20px;font-size:13.5px;}
.prose th{text-align:left;font:600 11px var(--mono);color:var(--txt-3);letter-spacing:.5px;text-transform:uppercase;padding:10px 14px;border-bottom:1px solid var(--line);background:var(--panel-2);}
.prose td{padding:11px 14px;border-bottom:1px solid var(--line-soft);color:var(--txt-2);vertical-align:top;}
.prose td code{font-size:12px;}
.prose hr{border:none;border-top:1px solid var(--line);margin:32px 0;}
.prose h2 a,.prose h3 a{color:inherit;}
.foot{margin-top:56px;padding-top:22px;border-top:1px solid var(--line-soft);font:500 12px var(--mono);color:var(--txt-3);}
@media(max-width:820px){
  .wrap{grid-template-columns:1fr;}
  .rail{position:static;height:auto;border-right:none;border-bottom:1px solid var(--line);}
  .prose{padding:0 24px;}
}
</style>
</head>
<body>
<div class="wrap">
  <aside class="rail">
    <a class="brand" href="/doc">mAPI-ng <span>docs</span></a>
    <p class="tagline">Zero-config RED metrics</p>
    {{range .Groups}}
    <div class="grp">{{.Group}}</div>
    {{range .Items}}<a class="lnk{{if .Active}} on{{end}}" href="{{.Href}}">{{.Title}}</a>{{end}}
    {{end}}
  </aside>
  <main class="main">
    <article class="prose">
      {{.Body}}
      <div class="foot">mAPI-ng · <a href="/">dashboard</a> · <a href="https://github.com/arhuman/maping">source</a></div>
    </article>
  </main>
</div>
</body>
</html>`))
