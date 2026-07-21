package docs

// This file holds the in-app rendering path: when the composition root wires
// EnableInApp (dashboard auth on), a signed-in visitor's doc page renders as a
// fragment inside the dashboard chrome instead of the standalone public shell in
// render.go. The fragment keeps the classic doc layout (grouped TOC rail + prose
// column) and carries its own scoped <style> block; it is kept in its own file
// because template literals count toward the 500-line checklen budget.

import (
	"bytes"
	"html/template"
	"log/slog"
	"net/http"
)

// EnableInApp wires the two collaborators the in-app rendering path needs:
// authed soft-checks the session cookie (no redirect), and render writes a
// trusted HTML fragment inside the authenticated dashboard chrome. The
// composition root calls this only when an auth layer exists; until then Render
// keeps the standalone shell for every request, so the community build (no
// control plane) is untouched.
func (h *Handler) EnableInApp(authed func(*http.Request) bool, render func(http.ResponseWriter, *http.Request, string, template.HTML)) {
	h.authed = authed
	h.renderApp = render
}

// appData is the in-app fragment's view model: the rendered prose body and the
// same grouped, active-lit table of contents the standalone shell shows.
type appData struct {
	Body   template.HTML
	Groups []navGroup
}

// renderInApp executes the fragment template and hands the result to the
// injected dashboard renderer, which wraps it in the sidebar + top-bar chrome
// with the Documentation nav item lit. A template failure logs and 500s (nothing
// has been written yet, so the error page is still deliverable).
func (h *Handler) renderInApp(w http.ResponseWriter, r *http.Request, title string, body template.HTML) {
	var buf bytes.Buffer
	if err := appTmpl.Execute(&buf, appData{Body: body, Groups: h.groups(r.URL.Path)}); err != nil {
		h.log.Error("docs: execute in-app fragment", slog.Any("err", err))
		http.Error(w, "documentation unavailable", http.StatusInternalServerError)
		return
	}
	h.renderApp(w, r, title, template.HTML(buf.String())) //nolint:gosec // template-produced, autoescaped output
}

// appTmpl is the in-app doc fragment: the two-column layout (grouped TOC rail +
// prose column) dropped into the dashboard's content area. No <html>/<head> and
// no scripts — the dashboard CSP allows inline styles but no inline JS. Every
// class is doc- prefixed so nothing can collide with the dashboard chrome
// classes, and the colours come from the CSS variables the dashboard head
// defines (--panel, --line, --txt*, --accent, --blue, --mono, --ui), so the
// fragment inherits the dashboard theme instead of restating it. The prose rules
// mirror the standalone shell's (render.go) so a page reads identically in both
// chromes.
var appTmpl = template.Must(template.New("docapp").Parse(`<style>
.doc-wrap{display:grid;grid-template-columns:240px minmax(0,1fr);align-items:start;}
.doc-rail{border-right:1px solid var(--line);padding:2px 14px 2px 0;}
.doc-grp{font:700 10px var(--mono);color:var(--txt-3);letter-spacing:1px;text-transform:uppercase;margin:20px 6px 8px;}
.doc-grp:first-child{margin-top:0;}
.doc-lnk{display:block;padding:7px 10px;border-radius:8px;font:600 13px var(--ui);color:var(--txt-2);}
.doc-lnk:hover{background:var(--panel-2);color:var(--txt);}
.doc-lnk.on{background:rgba(180,241,74,.10);color:var(--txt);}
.doc-prose{max-width:760px;padding:0 34px;}
.doc-prose h1{font:800 32px/1.15 var(--ui);letter-spacing:-.5px;margin:0 0 24px;}
.doc-prose h2{font:700 22px/1.2 var(--ui);letter-spacing:-.3px;margin:44px 0 14px;padding-top:14px;border-top:1px solid var(--line-soft);}
.doc-prose h3{font:700 16px var(--ui);margin:28px 0 10px;}
.doc-prose p{font:400 15px/1.7 var(--ui);color:var(--txt);margin:0 0 16px;}
.doc-prose li{font:400 15px/1.7 var(--ui);color:var(--txt);margin:6px 0;}
.doc-prose ul,.doc-prose ol{padding-left:22px;margin:0 0 16px;}
.doc-prose a{color:var(--blue);}
.doc-prose a:hover{text-decoration:underline;}
.doc-prose strong{color:var(--txt);font-weight:700;}
.doc-prose code{font:500 13px var(--mono);background:var(--panel-3);color:#D8E4C0;padding:2px 6px;border-radius:5px;}
.doc-prose pre{background:var(--panel);border:1px solid var(--line);border-radius:12px;padding:16px 18px;overflow-x:auto;margin:0 0 18px;}
.doc-prose pre code{background:none;padding:0;color:var(--txt);font-size:13px;line-height:1.6;}
.doc-prose blockquote{margin:0 0 18px;padding:12px 18px;border-left:3px solid var(--accent);background:var(--panel-2);border-radius:0 10px 10px 0;color:var(--txt-2);}
.doc-prose blockquote p{margin:0;font-size:14px;}
.doc-prose table{width:100%;border-collapse:collapse;margin:0 0 20px;font-size:13.5px;}
.doc-prose th{text-align:left;font:600 11px var(--mono);color:var(--txt-3);letter-spacing:.5px;text-transform:uppercase;padding:10px 14px;border-bottom:1px solid var(--line);background:var(--panel-2);}
.doc-prose td{padding:11px 14px;border-bottom:1px solid var(--line-soft);color:var(--txt-2);vertical-align:top;}
.doc-prose td code{font-size:12px;}
.doc-prose hr{border:none;border-top:1px solid var(--line);margin:32px 0;}
.doc-prose h2 a,.doc-prose h3 a{color:inherit;}
@media(max-width:980px){
  .doc-wrap{grid-template-columns:1fr;}
  .doc-rail{border-right:none;border-bottom:1px solid var(--line);padding:0 0 16px;margin-bottom:20px;}
  .doc-prose{padding:0;}
}
</style>
<div class="doc-wrap">
  <aside class="doc-rail">
    {{range .Groups}}
    <div class="doc-grp">{{.Group}}</div>
    {{range .Items}}<a class="doc-lnk{{if .Active}} on{{end}}" href="{{.Href}}">{{.Title}}</a>{{end}}
    {{end}}
  </aside>
  <article class="doc-prose">
    {{.Body}}
  </article>
</div>`))
