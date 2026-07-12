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

// contentSecurityPolicy pins the JS budget: script-src 'self' means the only
// script the browser will run is the self-hosted copy helper — no inline JS, no
// CDN scripts, no eval. Styles stay inline ('unsafe-inline') because the pages
// are heavily inline-styled server-rendered HTML, and the Google Fonts stylesheet
// and font files are allowlisted explicitly.
const contentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; " +
	"font-src https://fonts.gstatic.com; " +
	"img-src 'self' data:; " +
	"base-uri 'none'; frame-ancestors 'none'"

// serveCopyJS serves the embedded copy helper. It is static, so it carries a
// short cache lifetime; the CSP header keeps it the only executable script.
func (h *Handler) serveCopyJS(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
	if _, err := w.Write(copyJS); err != nil {
		h.log.Error("web: write copy.js", slog.Any("err", err))
	}
}
