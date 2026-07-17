package web

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arhuman/maping/server/internal/storage"
)

func TestOverviewLiveMetaByQuery(t *testing.T) {
	q := fakeQuerier{hasData: true, services: []storage.ServiceStat{
		{Service: "checkout-api", Count: 100},
	}}
	srv := newServer(t, Config{Querier: q, Tenant: constTenant})

	// Opt-in ?live=1 -> auto-refresh meta + "live" toggle state.
	_, live := getBody(t, srv.URL+"/?live=1")
	assert.Contains(t, live, `http-equiv="refresh"`)
	assert.Contains(t, live, `content="15"`)

	// Default (no ?live) -> static, no refresh meta, and the toggle offers ?live=1.
	_, static := getBody(t, srv.URL+"/")
	assert.NotContains(t, static, `http-equiv="refresh"`)
	assert.Contains(t, static, "live=1", "the paused indicator links to opt into live")
}

func TestOnboardingMetaRefreshWhileIncomplete(t *testing.T) {
	// No data -> the onboarding panel advances the handshake via the in-place
	// poller (no full-document flicker); the meta-refresh survives only inside a
	// <noscript> fallback for no-JS clients.
	srv := newServer(t, Config{Querier: fakeQuerier{hasData: false}, Tenant: constTenant})
	_, body := getBody(t, srv.URL+"/")
	assert.Contains(t, body, `<noscript><meta http-equiv="refresh" content="10"></noscript>`)
	// The refresh meta must NOT be emitted outside <noscript> (the source of the flicker).
	assert.NotContains(t, body, `<head><meta http-equiv="refresh"`)
	// JS clients poll the fragment in place: the container + poller script are present.
	assert.Contains(t, body, `id="handshake"`)
	assert.Contains(t, body, `data-complete="false"`)
	assert.Contains(t, body, `src="/assets/handshake.js"`)
}

func TestSetupMetaRefreshGatedOnData(t *testing.T) {
	// Incomplete onboarding -> Setup keeps the <noscript> fallback and the poller.
	inc := newServer(t, Config{Querier: fakeQuerier{hasData: false}, Tenant: constTenant})
	_, body := getBody(t, inc.URL+"/setup")
	assert.Contains(t, body, `<noscript><meta http-equiv="refresh" content="10"></noscript>`)
	assert.Contains(t, body, `data-complete="false"`)
	assert.Contains(t, body, `src="/assets/handshake.js"`)

	// Once data exists, Setup is a static management page: no refresh fallback, and
	// the poller short-circuits because the container is already complete.
	done := newServer(t, Config{Querier: fakeQuerier{hasData: true}, Tenant: constTenant})
	_, body2 := getBody(t, done.URL+"/setup")
	assert.NotContains(t, body2, `http-equiv="refresh"`)
	assert.Contains(t, body2, `data-complete="true"`)
}

func TestHandshakeFragmentReflectsData(t *testing.T) {
	// Incomplete -> fragment renders the stepper and signals not-complete so the
	// client keeps polling.
	inc := newServer(t, Config{Querier: fakeQuerier{hasData: false}, Tenant: constTenant})
	resp, err := http.Get(inc.URL + "/setup/handshake")
	require.NoError(t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "false", resp.Header.Get("X-Handshake-Complete"))
	body := string(b)
	assert.Contains(t, body, "HANDSHAKE")
	assert.Contains(t, body, "Ingest key valid")
	// The fragment is ONLY the stepper — no full document chrome.
	assert.NotContains(t, body, "<html")
	assert.NotContains(t, body, `id="handshake"`)

	// Complete -> fragment signals done so the client stops polling.
	done := newServer(t, Config{Querier: fakeQuerier{hasData: true}, Tenant: constTenant})
	resp2, err := http.Get(done.URL + "/setup/handshake")
	require.NoError(t, err)
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusOK, resp2.StatusCode)
	assert.Equal(t, "true", resp2.Header.Get("X-Handshake-Complete"))
}

func TestHandshakeFragmentShowsConnectedSources(t *testing.T) {
	src := func(context.Context, string) ([]ServiceOnboarding, error) {
		return []ServiceOnboarding{{Service: "checkout-api", Instance: "pod-7"}}, nil
	}
	srv := newServer(t, Config{Querier: fakeQuerier{hasData: false}, Tenant: constTenant, Onboarding: src})
	_, body := getBody(t, srv.URL+"/setup/handshake")
	assert.Contains(t, body, "CONNECTED")
	assert.Contains(t, body, "checkout-api")
	assert.Contains(t, body, "pod-7")
}

func TestHandshakeJSAssetServed(t *testing.T) {
	srv := newServer(t, Config{Querier: fakeQuerier{}, Tenant: constTenant})
	resp, err := http.Get(srv.URL + "/assets/handshake.js")
	require.NoError(t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/javascript; charset=utf-8", resp.Header.Get("Content-Type"))
	assert.Contains(t, string(b), "/setup/handshake")
	assert.Contains(t, resp.Header.Get("Content-Security-Policy"), "script-src 'self'")
}

func TestOverviewTriageSortForUnhealthy(t *testing.T) {
	// An unhealthy service (error rate >= warn) drills into the error-sorted table.
	unhealthy := newServer(t, Config{Tenant: constTenant, Querier: fakeQuerier{
		hasData:  true,
		services: []storage.ServiceStat{{Service: "checkout-api", Count: 1000, ErrorRate: 0.12}},
	}})
	_, body := getBody(t, unhealthy.URL+"/")
	assert.Contains(t, body, "/services/checkout-api?sort=error")

	// A healthy service drills to the default (traffic) sort — no sort hint.
	healthy := newServer(t, Config{Tenant: constTenant, Querier: fakeQuerier{
		hasData:  true,
		services: []storage.ServiceStat{{Service: "inventory", Count: 1000, ErrorRate: 0.0}},
	}})
	_, body2 := getBody(t, healthy.URL+"/")
	assert.NotContains(t, body2, "sort=error")
	// The drill href carries the active window so navigation preserves the lookback.
	assert.Contains(t, body2, `href="/services/inventory?win=1h"`)
}

func TestDetailRendersDebugContext(t *testing.T) {
	q := fakeQuerier{detail: storage.EndpointDetail{
		Count: 1000, ErrorRate: 0.01, P95: 0.21, P99: 0.48,
		StatusClasses: []storage.StatusClassCount{
			{Class: "2xx", Count: 990},
			{Class: "4xx", Count: 3},
			{Class: "5xx", Count: 7},
		},
	}}
	srv := newServer(t, Config{Querier: q, Tenant: constTenant})

	_, body := getBody(t, srv.URL+"/services/checkout-api/endpoint?method=GET&route=/orders")
	assert.Contains(t, body, "DEBUG CONTEXT")
	assert.Contains(t, body, "checkout-api")
	assert.Contains(t, body, "GET")
	assert.Contains(t, body, "/orders")
	assert.Contains(t, body, "dominant error 5xx") // 5xx (7) beats 4xx (3)
	assert.Contains(t, body, "p95 210 ms")
	assert.Contains(t, body, "p99 480 ms")
	// The copy button + helper make the block one-click shareable.
	assert.Contains(t, body, `data-copy="mp-debug"`)
	assert.Contains(t, body, `src="/assets/copy.js"`)
}

func TestDetailDebugContextNoErrors(t *testing.T) {
	q := fakeQuerier{detail: storage.EndpointDetail{
		Count: 500, P95: 0.05, P99: 0.09,
		StatusClasses: []storage.StatusClassCount{{Class: "2xx", Count: 500}},
	}}
	srv := newServer(t, Config{Querier: q, Tenant: constTenant})
	_, body := getBody(t, srv.URL+"/services/svc/endpoint?method=GET&route=/ok")
	assert.Contains(t, body, "dominant error none")
}

// guard against the /api handlers accidentally gaining the refresh meta.
func TestPerformanceHasNoRefreshMeta(t *testing.T) {
	srv := newServer(t, Config{Querier: fakeQuerier{hasData: true}, Tenant: constTenant})
	code, body := getBody(t, srv.URL+"/performance")
	assert.Equal(t, http.StatusOK, code)
	assert.NotContains(t, body, `http-equiv="refresh"`)
}

func TestPerformancePageShowsRealData(t *testing.T) {
	q := fakeQuerier{
		hasData:     true,
		performance: storage.PerformanceStat{Requests: 4_400_000, Summaries: 1000, SummaryDiskBytes: 400_000},
	}
	srv := newServer(t, Config{Querier: q, Tenant: constTenant})
	code, body := getBody(t, srv.URL+"/performance")
	assert.Equal(t, http.StatusOK, code)
	// The measured compression and represented-request count reach the HTML.
	assert.Contains(t, body, "4.4k×")
	assert.Contains(t, body, "4.4M")
	// The old hardcoded illustrative figures are gone.
	assert.NotContains(t, body, "47.2 GB")
	assert.NotContains(t, body, "182")
}

func TestPerformancePageEmptyState(t *testing.T) {
	srv := newServer(t, Config{Querier: fakeQuerier{}, Tenant: constTenant})
	// The page honours the selected window: the empty-state copy names it.
	code, body := getBody(t, srv.URL+"/performance?win=24h")
	assert.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, "No summaries stored in the last 24 hours yet")
	// The window switcher is shown on Performance (it drives the figures).
	assert.Contains(t, body, "/performance?win=5m")
}
