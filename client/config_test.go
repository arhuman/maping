package maping

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestWithServiceOption verifies WithService sets Config.Service, overriding
// any env-derived value.
func TestWithServiceOption(t *testing.T) {
	clearMapingEnv(t)
	cfg := resolveConfig([]Option{WithService("my-service")})
	assert.Equal(t, "my-service", cfg.Service)
}

// TestWithInstanceOption verifies WithInstance sets Config.Instance, overriding
// any env-derived value.
func TestWithInstanceOption(t *testing.T) {
	clearMapingEnv(t)
	cfg := resolveConfig([]Option{WithInstance("my-instance")})
	assert.Equal(t, "my-instance", cfg.Instance)
}

// TestDeriveInstanceBranches exercises all three resolution branches of
// deriveInstance: MAPING_INSTANCE, HOSTNAME, and os.Hostname fallback.
func TestDeriveInstanceBranches(t *testing.T) {
	tests := []struct {
		name         string
		mapingInst   string
		hostname     string
		wantExact    string
		wantFallback bool // accept any non-empty value (os.Hostname)
	}{
		{
			name:       "MAPING_INSTANCE wins",
			mapingInst: "inst-from-env",
			hostname:   "host-fallback",
			wantExact:  "inst-from-env",
		},
		{
			name:       "HOSTNAME used when MAPING_INSTANCE unset",
			mapingInst: "",
			hostname:   "docker-hostname",
			wantExact:  "docker-hostname",
		},
		{
			name:         "os.Hostname fallback when both envs empty",
			mapingInst:   "",
			hostname:     "",
			wantFallback: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearMapingEnv(t)
			t.Setenv("MAPING_INSTANCE", tt.mapingInst)
			t.Setenv("HOSTNAME", tt.hostname)

			got := deriveInstance()

			if tt.wantExact != "" {
				assert.Equal(t, tt.wantExact, got)
			} else if tt.wantFallback {
				// The fallback is os.Hostname(); accept whatever it returns as
				// long as it is non-empty (or "unknown" if the OS call fails).
				h, _ := os.Hostname()
				if h == "" {
					assert.Equal(t, "unknown", got, "no hostname available: must return 'unknown'")
				} else {
					assert.NotEmpty(t, got)
				}
			}
		})
	}
}
