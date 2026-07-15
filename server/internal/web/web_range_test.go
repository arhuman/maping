package web

import (
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arhuman/maping/server/internal/storage"
)

func TestResolveDetailRange(t *testing.T) {
	now := time.Now().UTC()
	from := now.Add(-90 * time.Minute)
	to := now.Add(-30 * time.Minute)

	t.Run("valid from/to overrides the preset", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/x?from="+ts(from)+"&to="+ts(to), nil)
		gotFrom, gotTo, custom := resolveDetailRange(r, time.Hour)
		require.True(t, custom)
		assert.Equal(t, from.Unix(), gotFrom.Unix())
		assert.Equal(t, to.Unix(), gotTo.Unix())
	})

	t.Run("missing params fall back to the preset window", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/x", nil)
		_, _, custom := resolveDetailRange(r, time.Hour)
		assert.False(t, custom)
	})

	t.Run("malformed params fall back", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/x?from=abc&to=def", nil)
		_, _, custom := resolveDetailRange(r, time.Hour)
		assert.False(t, custom)
	})

	t.Run("a future trailing edge is clamped to now", func(t *testing.T) {
		future := now.Add(2 * time.Hour)
		r := httptest.NewRequest("GET", "/x?from="+ts(from)+"&to="+ts(future), nil)
		_, gotTo, custom := resolveDetailRange(r, time.Hour)
		require.True(t, custom)
		assert.False(t, gotTo.After(time.Now().UTC()), "to must not be in the future")
	})

	t.Run("a sub-10s range is rejected", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/x?from="+ts(now)+"&to="+ts(now.Add(3*time.Second)), nil)
		_, _, custom := resolveDetailRange(r, time.Hour)
		assert.False(t, custom)
	})
}

func TestAdaptiveStep(t *testing.T) {
	assert.Equal(t, time.Minute, adaptiveStep(time.Hour), "1h / 60 buckets = 1m")
	assert.Equal(t, 2*time.Minute, adaptiveStep(2*time.Hour))
	assert.Equal(t, 10*time.Second, adaptiveStep(6*time.Minute), "floored at the 10s raw resolution")
}

func TestBuildDetailRange(t *testing.T) {
	now := time.Now().UTC()

	t.Run("preset offers zoom/pan but no reset and no future pan", func(t *testing.T) {
		from, to := now.Add(-time.Hour), now
		dr := buildDetailRange("svc", "GET", "/orders", "1h", from, to, now, false)
		assert.False(t, dr.Custom)
		assert.Empty(t, dr.ResetHref, "nothing to reset from a preset")
		assert.Empty(t, dr.PanRightHref, "cannot pan into the future when flush with now")
		assert.NotEmpty(t, dr.ZoomOutHref)
		assert.NotEmpty(t, dr.PanLeftHref)
		assert.Equal(t, "1 hour", dr.Label)
	})

	t.Run("custom range carries a reset and a future pan", func(t *testing.T) {
		from, to := now.Add(-90*time.Minute), now.Add(-30*time.Minute)
		dr := buildDetailRange("svc", "GET", "/orders", "1h", from, to, now, true)
		require.True(t, dr.Custom)
		assert.NotEmpty(t, dr.ResetHref)
		assert.NotContains(t, dr.ResetHref, "from=", "reset drops the custom range")
		assert.Contains(t, dr.ResetHref, "win=1h")
		assert.NotEmpty(t, dr.PanRightHref, "headroom to pan forward exists")
		assert.Contains(t, dr.ZoomOutHref, "from=")
		assert.Contains(t, dr.ZoomOutHref, "route=%2Forders", "route is url-escaped")
	})
}

func TestDetailRangeControlsRender(t *testing.T) {
	q := fakeQuerier{detail: storage.EndpointDetail{Count: 100, P95: 0.2}}
	srv := newServer(t, Config{Querier: q, Tenant: constTenant})

	// Preset: the window label + zoom-out control show; no reset.
	_, body := getBody(t, srv.URL+"/services/checkout/endpoint?method=GET&route=/orders")
	assert.Contains(t, body, "zoom out")
	assert.Contains(t, body, "window")

	// Custom range: the pill + reset link show.
	now := time.Now().UTC()
	url := srv.URL + "/services/checkout/endpoint?method=GET&route=/orders" +
		"&from=" + ts(now.Add(-time.Hour)) + "&to=" + ts(now.Add(-30*time.Minute))
	_, custom := getBody(t, url)
	assert.Contains(t, custom, "reset")
	assert.Contains(t, custom, "⤢")
}

// ts renders a time as unix-seconds for a query param.
func ts(t time.Time) string { return strconv.FormatInt(t.Unix(), 10) }
