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

// TestDeployIdentityFromEnv verifies the deploy dimension env vars are parsed
// into Config.
func TestDeployIdentityFromEnv(t *testing.T) {
	clearMapingEnv(t)
	t.Setenv("MAPING_DEPLOY_VERSION", "v1.2.3")
	t.Setenv("MAPING_DEPLOY_ID", "abc123sha")
	t.Setenv("MAPING_ENVIRONMENT", "staging")
	t.Setenv("MAPING_REGION", "eu-west-1")

	cfg := resolveConfig(nil)

	assert.Equal(t, "v1.2.3", cfg.DeployVersion)
	assert.Equal(t, "abc123sha", cfg.DeployID)
	assert.Equal(t, "staging", cfg.Environment)
	assert.Equal(t, "eu-west-1", cfg.Region)
}

// TestDeployIdentityOptionsBeatEnv verifies the deploy Options override the
// matching env vars (option > env, CONFIG.md precedence).
func TestDeployIdentityOptionsBeatEnv(t *testing.T) {
	clearMapingEnv(t)
	t.Setenv("MAPING_DEPLOY_VERSION", "env-version")
	t.Setenv("MAPING_DEPLOY_ID", "env-id")
	t.Setenv("MAPING_ENVIRONMENT", "env-env")
	t.Setenv("MAPING_REGION", "env-region")

	cfg := resolveConfig([]Option{
		WithDeployVersion("opt-version"),
		WithDeployID("opt-id"),
		WithEnvironment("opt-env"),
		WithRegion("opt-region"),
	})

	assert.Equal(t, "opt-version", cfg.DeployVersion)
	assert.Equal(t, "opt-id", cfg.DeployID)
	assert.Equal(t, "opt-env", cfg.Environment)
	assert.Equal(t, "opt-region", cfg.Region)
}

// TestDeriveDeployIDDefaultsToVCS verifies deploy_id, when neither the env var
// nor an option is set, defaults to the VCS revision from the build info. Under
// `go test` the build info may or may not carry a vcs.revision, so the contract
// asserted is that deriveDeployID mirrors vcsRevision() exactly when the env is
// unset (empty when unavailable, the SHA when available).
func TestDeriveDeployIDDefaultsToVCS(t *testing.T) {
	clearMapingEnv(t)
	assert.Equal(t, vcsRevision(), deriveDeployID())
}

// TestDeriveDeployIDEnvWins verifies MAPING_DEPLOY_ID takes precedence over the
// VCS-revision default.
func TestDeriveDeployIDEnvWins(t *testing.T) {
	clearMapingEnv(t)
	t.Setenv("MAPING_DEPLOY_ID", "explicit-build-id")
	assert.Equal(t, "explicit-build-id", deriveDeployID())
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
