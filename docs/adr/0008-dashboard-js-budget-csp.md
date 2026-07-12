---
status: accepted
---

# Dashboard JS budget: SSR plus one copy-only helper under a strict script-src

The dashboard is fully server-rendered with no client JavaScript for navigation,
sorting, window switching, live refresh, or charting (those use plain links, query
parameters, meta-refresh, and inline SVG). A single client-side script, `copy.js`,
is permitted as the sole exception. A Content-Security-Policy header on every HTML
response enforces this budget at the browser level.

## Context

The dashboard renders fixed, non-configurable RED views using Go's `html/template`
with autoescaping. Navigation, sort order, and time-window switching are plain
`<a>` links. Live refresh is a `<meta http-equiv="refresh">`. Charts are inline SVG
produced server-side in `chart.go`. There is no client-side state.

Copy-to-clipboard has no server-side or CSS equivalent. It is required in three
places: the signup key interstitial (first-login reveal-once), the Setup page
reveal-once banner (after a `POST /setup/keys`), and the endpoint-detail debug
context block.

## Decision

**One self-hosted script.** The dashboard ships exactly one client-side script:
`assets/copy.js` (~15 lines). It registers a delegated `click` listener on the
document; when a `[data-copy]` element is clicked it copies the `textContent` of
the element whose `id` matches the attribute value via `navigator.clipboard.writeText`.
No client state, no DOM mutations beyond the transient "copied" label. The file is
embedded with `//go:embed assets/copy.js` and served at `GET /assets/copy.js`
from `serveCopyJS`.

**Content-Security-Policy on every HTML response.** The `render` method in `web.go`
sets the header on every dashboard HTML response, and `renderKeyInterstitial` in
`auth/handlers.go` sets it on the signup interstitial. The constant policy strings
in `assets.go` (`contentSecurityPolicy`) and `auth/handlers.go` (`interstitialCSP`)
are identical:

```
default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src https://fonts.gstatic.com; img-src 'self' data:; base-uri 'none'; frame-ancestors 'none'
```

The load-bearing directive is `script-src 'self'`: the only script the browser
executes is the self-hosted copy helper. Inline JS, CDN scripts, and `eval` are all
blocked.

**`style-src` retains `'unsafe-inline'` as accepted debt.** The pages are heavily
inline-styled server-rendered HTML. Allowlisting `https://fonts.googleapis.com`
and `font-src https://fonts.gstatic.com` covers the Google Fonts stylesheet and
font files used by the dashboard.

**Graceful degradation.** In both the interstitial and the Setup reveal banner, the
token element carries `user-select:all` so a user without clipboard API support (or
with JS disabled) can still select the full token with a single click and copy it
manually.

## Why a single self-hosted script

Self-hosting is what makes `script-src 'self'` airtight. A CDN URL would require
adding that origin to `script-src`, widening the attack surface. The copy helper is
the one sanctioned exception to the no-JS budget because clipboard access has no
server-side equivalent. Every other interactive feature (sort, window, live toggle)
uses URL state and plain links, keeping the JS surface at its minimum.

## Consequences

Any new interactive feature must be implemented server-side or must justify an
addition to this JS budget with an ADR update. `style-src 'unsafe-inline'` is
accepted debt; future hardening could move inline styles to CSS classes or
nonce-based allowlisting.
