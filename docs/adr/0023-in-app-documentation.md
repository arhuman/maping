---
status: accepted
---

# In-app product documentation at /doc

The core server serves its own user-facing product documentation as
server-rendered pages at **`/doc`** (index) and **`/doc/{topic}`**, sourced from
Markdown embedded in a new importable `server/docs` package. The pages are public
(no auth), always mounted — the community build included — so the documentation is
never empty and never a dead external link.

## Why

The only "documentation" a visitor could reach was a link into the GitHub source
tree (the `client/` directory). From a user's path that reads as "there are no real
docs": the README, quickstart, `context.md` glossary and ADRs exist, but they are
either engineer-facing or off-site. The product needs a small, user-oriented docs
surface that lives in the running server.

Documentation about the core RED-metrics product (what data is collected, runtime
overhead, failure/retry behaviour, security and data flow, self-hosting,
architecture, benchmarks, licensing) describes the **core**, so it belongs in the
core repo and must exist in every build, not only behind the commercial binary.

## Decisions

- **Markdown, embedded.** Pages are authored as `.md` under `server/docs/content/`
  and embedded via `//go:embed`. Prose is edited as prose, not as Go string
  literals, and reuses the README / `context.md` text almost verbatim. Rendering
  uses `goldmark` (GFM + auto heading IDs) in its safe default (raw HTML in a
  source file is escaped), so a future externally sourced page cannot inject
  markup.
- **Standalone shell for anonymous visitors, dashboard chrome for signed-in
  users.** For an anonymous request `/doc` renders in a standalone dark shell (its
  own inline CSS, the product palette and fonts) with a left table-of-contents
  rail — docs must render without an org/session context, and in the community
  build (no auth layer) every request takes this path. These pages carry a
  `script-src 'none'` CSP — pure HTML + CSS, no JavaScript. When dashboard auth is
  on and the request carries a valid session, the same pages render **inside the
  dashboard chrome** instead: the composition root wires
  `docs.Handler.EnableInApp(authLayer.Authenticated, ...)`, and `Render` hands the
  doc fragment (same grouped TOC rail + prose, `doc-`-prefixed scoped styles) to
  `web.RenderDocPage`, which lights the Documentation sidebar item and adds a
  `docs → <title>` breadcrumb. The render func wraps itself in the auth middleware
  so the verified session lands in the request context (sidebar tenant and
  ingest-key mask); it cannot redirect, since it only runs after `Authenticated`
  reported the cookie valid. Clicking Documentation therefore never drops a
  signed-in user out of the dashboard onto a marketing-looking page.
- **Two composition seams (extends ADR-0016).** So the enterprise binary can add
  its own doc pages without forking:
  - `WithDocSections(...docs.Section)` injects entries into the shared table of
    contents, so an extension's topics appear in the rail on every doc page.
  - `RouteContext.RenderDoc(w, r, title, body)` wraps an extension-produced body
    (rendered from its own embedded Markdown via the exported
    `docs.MarkdownToHTML`) in the exact same shell and merged TOC. An extension
    page is therefore indistinguishable from a core one — including the
    anonymous/signed-in split above, since both flow through the same `Render`.
  - `WithDocHeader(template.HTML)` injects a full site header rendered above every
    doc page, so the documentation wears the same chrome as the rest of the site
    (same logo, nav, and calls to action) rather than a detached bar — the fix for
    "the docs read as a different site." The composing build passes its own
    marketing header (with absolute links); the community build sets none and the
    shell falls back to a minimal home brand (`/`), so no link ever points at a
    route the build does not serve.
  The `server/docs` package is intentionally **not** `internal/`, so a composing
  module can import `Section` / `MarkdownToHTML`.
- **Discoverability.** The doc pages carry a full site header (injected, or a
  minimal home brand in the community build) and a left TOC rail. The authenticated
  dashboard also links to `/doc` from a "Documentation" item in its sidebar
  (`buildNav`), so a signed-in user reaches the docs from every dashboard page — in
  the community build and the enterprise binary alike, since both render the same
  core dashboard chrome.
- **Routing.** `/doc` and `/doc/{topic}` mount on the outer (unauthenticated) mux
  like `/login`. Extension pages mount on more specific patterns (`/doc/billing`),
  which take Go 1.22 ServeMux precedence over the `{topic}` wildcard; an unknown
  core slug is a 404, never a blank shell.

## Consequences

- The docs are a first-class core feature: a lone self-hosted/community deployment
  serves the full product documentation with no configuration.
- Adding or editing a core page is editing one Markdown file; the section registry
  (`coreSections`) controls its title and order in the rail.
- The enterprise binary owns only its *additional* pages and their section entries;
  it does not duplicate core doc content (see enterprise ADR-0009).
- A new dependency (`goldmark`) enters the core module — a small, widely used,
  pure-Go Markdown library with no transitive service dependencies.
