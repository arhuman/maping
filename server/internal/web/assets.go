package web

import (
	_ "embed"
	"log/slog"
	"net/http"
)

// copyJS is the copy-to-clipboard helper — the single client-side script the
// dashboard ships (CONTEXT: SSR/no-JS, one exception). Embedded so it is served
// self-hosted under CSP script-src 'self'.
//
//go:embed assets/copy.js
var copyJS []byte

// handshakeJS polls /setup/handshake to refresh just the onboarding stepper in
// place while onboarding is incomplete, replacing the old full-page meta-refresh
// (which flickered the dark theme). Embedded so it is served self-hosted under
// CSP script-src 'self'.
//
//go:embed assets/handshake.js
var handshakeJS []byte

// contentSecurityPolicy pins the JS budget: script-src 'self' means the only
// scripts the browser will run are the self-hosted copy and handshake helpers —
// no inline JS, no CDN scripts, no eval. Styles stay inline ('unsafe-inline') because the pages
// are heavily inline-styled server-rendered HTML, and the Google Fonts stylesheet
// and font files are allowlisted explicitly.
const contentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; " +
	"font-src https://fonts.gstatic.com; " +
	"img-src 'self' data:; " +
	"base-uri 'none'; frame-ancestors 'none'"

// serveJS returns a handler serving a static embedded script (name is used only
// for the error log). The scripts are static, so they carry a short cache
// lifetime; the CSP header keeps them the only executable scripts the browser runs.
func (h *Handler) serveJS(name string, body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
		if _, err := w.Write(body); err != nil {
			h.log.Error("web: write "+name, slog.Any("err", err))
		}
	}
}
