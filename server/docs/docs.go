// Package docs serves the public, server-rendered product documentation at /doc.
// It owns the doc shell (its own dark chrome, no dashboard auth), a Markdown
// renderer, and the table-of-contents model. Core registers its own product pages
// from embedded Markdown; a composing build adds pages of its own by injecting
// Sections (for the shared left rail) and rendering its Markdown through the
// RenderDoc capability the app hands it — so every doc page looks identical
// whichever module served it.
package docs

import (
	"embed"
	"html/template"
	"log/slog"
	"net/http"
	"sort"
)

// content holds the core product docs. index.md is the /doc landing body; every
// other <slug>.md is served at /doc/<slug>.
//
//go:embed content/*.md
var content embed.FS

// Section is one entry in the docs left rail. Core contributes its own product
// sections; a composing build adds more via app.WithDocSections so its pages are
// discoverable from every doc page's table of contents.
type Section struct {
	// Group is the rail heading the link sits under (e.g. "Product", "Enterprise").
	Group string
	// Title is the link text; Href is the absolute path it points at ("/doc/quickstart").
	Title, Href string
	// Order sorts links within a group; groups themselves keep first-seen order.
	Order int
}

// coreSections is the built-in product table of contents — the nine topics that
// describe the core RED-metrics server, in reading order. They exist in every
// build (community included), so the docs are never empty.
var coreSections = []Section{
	{Group: "Product", Title: "Quickstart", Href: "/doc/quickstart", Order: 1},
	{Group: "Product", Title: "What data is collected", Href: "/doc/data-collected", Order: 2},
	{Group: "Product", Title: "Runtime overhead", Href: "/doc/runtime-overhead", Order: 3},
	{Group: "Product", Title: "Failure & retry behaviour", Href: "/doc/failure-retry", Order: 4},
	{Group: "Product", Title: "Security & data flow", Href: "/doc/security-data-flow", Order: 5},
	{Group: "Product", Title: "Self-hosting", Href: "/doc/self-hosting", Order: 6},
	{Group: "Product", Title: "Architecture", Href: "/doc/architecture", Order: 7},
	{Group: "Product", Title: "Benchmarks", Href: "/doc/benchmarks", Order: 8},
	{Group: "Product", Title: "Licensing", Href: "/doc/licensing", Order: 9},
}

// titleOf resolves a slug back to its rail title so a topic page's <title> and the
// dashboard tab read the human name, not the slug. Falls back to the slug.
func titleOf(sections []Section, href string) string {
	for _, s := range sections {
		if s.Href == href {
			return s.Title
		}
	}
	return ""
}

// Handler serves the doc pages. nav is the merged, ordered table of contents
// (core + injected sections) shared by every page.
type Handler struct {
	nav []Section
	log *slog.Logger
}

// NewHandler builds the doc handler, merging the injected extension sections after
// the core ones. The merged set drives the left rail on every page, so an
// extension page and a core page show the same table of contents.
func NewHandler(extra []Section, log *slog.Logger) *Handler {
	nav := make([]Section, 0, len(coreSections)+len(extra))
	nav = append(nav, coreSections...)
	nav = append(nav, extra...)
	return &Handler{nav: nav, log: log}
}

// Register mounts the doc routes on the public mux. /doc/{topic} is the core
// content; a composing build's own pages mount on more specific patterns
// (/doc/billing) that take ServeMux precedence over the {topic} wildcard.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /doc", h.index)
	mux.HandleFunc("GET /doc/{topic}", h.topic)
}

// index renders the /doc landing (content/index.md) inside the shell.
func (h *Handler) index(w http.ResponseWriter, r *http.Request) {
	h.serveFile(w, r, "index", "/doc")
}

// topic renders one core product page (content/<slug>.md); an unknown slug is a
// 404, never a blank shell.
func (h *Handler) topic(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("topic")
	h.serveFile(w, r, slug, "/doc/"+slug)
}

// serveFile reads an embedded Markdown file, renders it, and wraps it in the shell
// with activeHref lit in the rail. A missing file 404s.
func (h *Handler) serveFile(w http.ResponseWriter, r *http.Request, slug, activeHref string) {
	src, err := content.ReadFile("content/" + slug + ".md")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	body, err := MarkdownToHTML(src)
	if err != nil {
		h.log.Error("docs: render markdown", slog.String("slug", slug), slog.Any("err", err))
		http.Error(w, "documentation unavailable", http.StatusInternalServerError)
		return
	}
	title := titleOf(h.nav, activeHref)
	if title == "" {
		title = "Documentation"
	}
	h.Render(w, r, title, body)
}

// Render wraps a rendered body in the doc shell with the rail lit for the current
// request path. It is the capability the app exposes as RouteContext.RenderDoc so
// a composing build renders its own pages in this exact chrome, with the shared
// table of contents, without importing this package's internals.
func (h *Handler) Render(w http.ResponseWriter, r *http.Request, title string, body template.HTML) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
	data := shellData{Title: title, Body: body, Groups: h.groups(r.URL.Path)}
	if err := shellTmpl.Execute(w, data); err != nil {
		h.log.Error("docs: execute shell", slog.Any("err", err))
	}
}

// groups folds the flat nav into ordered groups for the template, lighting the
// link whose Href matches the current path.
func (h *Handler) groups(activePath string) []navGroup {
	order := []string{}
	byGroup := map[string][]navLink{}
	for _, s := range h.nav {
		if _, seen := byGroup[s.Group]; !seen {
			order = append(order, s.Group)
		}
		byGroup[s.Group] = append(byGroup[s.Group], navLink{Title: s.Title, Href: s.Href, Active: s.Href == activePath})
	}
	out := make([]navGroup, 0, len(order))
	for _, g := range order {
		items := byGroup[g]
		sort.SliceStable(items, func(i, j int) bool { return h.orderOf(g, items[i].Href) < h.orderOf(g, items[j].Href) })
		out = append(out, navGroup{Group: g, Items: items})
	}
	return out
}

// orderOf returns a section's Order for stable in-group sorting.
func (h *Handler) orderOf(group, href string) int {
	for _, s := range h.nav {
		if s.Group == group && s.Href == href {
			return s.Order
		}
	}
	return 0
}

// contentSecurityPolicy for the doc pages: no scripts at all (the pages are pure
// server-rendered HTML + CSS), Google Fonts allowlisted like the dashboard.
const contentSecurityPolicy = "default-src 'self'; " +
	"script-src 'none'; " +
	"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; " +
	"font-src https://fonts.gstatic.com; " +
	"img-src 'self' data:; " +
	"base-uri 'none'; frame-ancestors 'none'"
