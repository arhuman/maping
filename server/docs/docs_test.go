package docs

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testHandler(extra ...Section) (*Handler, *http.ServeMux) {
	h := NewHandler(extra, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	mux := http.NewServeMux()
	h.Register(mux)
	return h, mux
}

func get(t *testing.T, mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestIndexRendersWithTableOfContents(t *testing.T) {
	_, mux := testHandler()
	rec := get(t, mux, "/doc")
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "Quickstart", "core sections must appear in the rail")
	assert.Contains(t, body, `href="/doc/architecture"`)
	assert.Equal(t, contentSecurityPolicy, rec.Header().Get("Content-Security-Policy"))
}

func TestTopicRendersKnownSlug(t *testing.T) {
	_, mux := testHandler()
	rec := get(t, mux, "/doc/quickstart")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `class="prose"`)
}

func TestUnknownTopicIs404(t *testing.T) {
	_, mux := testHandler()
	rec := get(t, mux, "/doc/does-not-exist")
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestInjectedSectionAppearsInRail(t *testing.T) {
	_, mux := testHandler(Section{Group: "Enterprise", Title: "Billing", Href: "/doc/billing", Order: 1})
	rec := get(t, mux, "/doc")
	body := rec.Body.String()
	assert.Contains(t, body, "Enterprise", "injected group heading must render")
	assert.Contains(t, body, `href="/doc/billing"`)
	// The core group must still precede the injected one (first-seen group order).
	assert.Less(t, strings.Index(body, "Product"), strings.Index(body, "Enterprise"))
}

func TestRenderWrapsArbitraryBodyWithSharedNav(t *testing.T) {
	h, _ := testHandler(Section{Group: "Enterprise", Title: "Billing", Href: "/doc/billing", Order: 1})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/doc/billing", nil)
	body, err := MarkdownToHTML([]byte("# Billing\n\nHow billing works."))
	require.NoError(t, err)
	h.Render(rec, req, "Billing", body)
	out := rec.Body.String()
	assert.Contains(t, out, "How billing works.", "extension body must be embedded")
	assert.Contains(t, out, "Quickstart", "shared core TOC must still render on an extension page")
	// The active link for the current path is lit.
	assert.Contains(t, out, `class="lnk on" href="/doc/billing"`)
}

func TestInjectedHeaderReplacesBuiltInBar(t *testing.T) {
	h := NewHandler(nil, `<header id="site-header"><a href="/pricing">Pricing</a></header>`, slog.New(slog.NewTextHandler(io.Discard, nil)))
	mux := http.NewServeMux()
	h.Register(mux)
	body := get(t, mux, "/doc").Body.String()
	assert.Contains(t, body, `id="site-header"`, "the injected site header must render")
	assert.Contains(t, body, `href="/pricing"`)
	assert.NotContains(t, body, `class="topbar"`, "the minimal built-in bar is replaced when a header is injected")
}

func TestCommunityBuildFallsBackToHomeBrand(t *testing.T) {
	// No injected header: the shell shows only the minimal home brand, never a dead
	// link to a route the build does not serve.
	_, mux := testHandler()
	body := get(t, mux, "/doc").Body.String()
	assert.Contains(t, body, `class="topbar"`)
	assert.Contains(t, body, `class="b" href="/"`)
	assert.NotContains(t, body, `/pricing`)
}

func TestMarkdownRendersTablesAndCode(t *testing.T) {
	out, err := MarkdownToHTML([]byte("| a | b |\n|---|---|\n| 1 | 2 |\n\n`code`"))
	require.NoError(t, err)
	s := string(out)
	assert.Contains(t, s, "<table>")
	assert.Contains(t, s, "<code>code</code>")
}
