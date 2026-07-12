package ingest

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTenantLimiterAllowsBurstThenThrottles(t *testing.T) {
	// 1 rps, burst 3: first 3 allowed, 4th denied within the same instant.
	lim := newTenantLimiter(1, 3)
	assert.True(t, lim.allow("t1"))
	assert.True(t, lim.allow("t1"))
	assert.True(t, lim.allow("t1"))
	assert.False(t, lim.allow("t1"), "burst exhausted, should throttle")
}

func TestTenantLimiterIsolatesTenants(t *testing.T) {
	lim := newTenantLimiter(1, 1)
	assert.True(t, lim.allow("t1"))
	assert.False(t, lim.allow("t1"))
	// A different tenant has its own bucket.
	assert.True(t, lim.allow("t2"))
}
