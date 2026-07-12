package web

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

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
	// No data -> the onboarding panel self-refreshes to advance the handshake.
	srv := newServer(t, Config{Querier: fakeQuerier{hasData: false}, Tenant: constTenant})
	_, body := getBody(t, srv.URL+"/")
	assert.Contains(t, body, `http-equiv="refresh"`)
	assert.Contains(t, body, `content="3"`)
}

func TestSetupMetaRefreshGatedOnData(t *testing.T) {
	// Incomplete onboarding -> Setup refreshes to advance the stepper.
	inc := newServer(t, Config{Querier: fakeQuerier{hasData: false}, Tenant: constTenant})
	_, body := getBody(t, inc.URL+"/setup")
	assert.Contains(t, body, `content="3"`)

	// Once data exists, Setup is a static management page (no refresh loop).
	done := newServer(t, Config{Querier: fakeQuerier{hasData: true}, Tenant: constTenant})
	_, body2 := getBody(t, done.URL+"/setup")
	assert.NotContains(t, body2, `http-equiv="refresh"`)
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
		services: []storage.ServiceStat{{Service: "billing", Count: 1000, ErrorRate: 0.0}},
	}})
	_, body2 := getBody(t, healthy.URL+"/")
	assert.NotContains(t, body2, "sort=error")
	assert.Contains(t, body2, `href="/services/billing"`)
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
	// The copy button + helper (slice 6) make the block one-click shareable.
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
