package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionKeyFromEnv(t *testing.T) {
	t.Setenv("MAPING_SESSION_KEY", "0123456789abcdef0123456789abcdef") // 32 bytes
	// A configured key is honored regardless of the production signal.
	key, err := sessionKey(true, testLogger())
	require.NoError(t, err)
	assert.Equal(t, []byte("0123456789abcdef0123456789abcdef"), key)
}

func TestSessionKeyTooShort(t *testing.T) {
	t.Setenv("MAPING_SESSION_KEY", "too-short")
	_, err := sessionKey(false, testLogger())
	require.Error(t, err, "a key shorter than 32 bytes must be rejected")
}

func TestSessionKeyEphemeralInDev(t *testing.T) {
	t.Setenv("MAPING_SESSION_KEY", "")
	// Local http dev (requireStable=false) generates an ephemeral key.
	key, err := sessionKey(false, testLogger())
	require.NoError(t, err)
	assert.Len(t, key, 32, "an unset key generates a 32-byte ephemeral key in dev")
}

func TestSessionKeyRequiredInProd(t *testing.T) {
	t.Setenv("MAPING_SESSION_KEY", "")
	// Production (https base URL) refuses to start rather than mint an ephemeral
	// key that would drop every session on restart.
	_, err := sessionKey(true, testLogger())
	require.Error(t, err, "an https deployment must require a stable session key")
}

func TestMaxBodyCeiling(t *testing.T) {
	t.Run("default when unset", func(t *testing.T) {
		t.Setenv("MAPING_MAX_BODY_BYTES", "")
		assert.Equal(t, int64(defaultMaxBodyCeiling), maxBodyCeiling(testLogger()))
	})
	t.Run("valid override", func(t *testing.T) {
		t.Setenv("MAPING_MAX_BODY_BYTES", "1048576")
		assert.Equal(t, int64(1048576), maxBodyCeiling(testLogger()))
	})
	t.Run("invalid falls back to default", func(t *testing.T) {
		t.Setenv("MAPING_MAX_BODY_BYTES", "not-a-number")
		assert.Equal(t, int64(defaultMaxBodyCeiling), maxBodyCeiling(testLogger()))
	})
	t.Run("non-positive falls back to default", func(t *testing.T) {
		t.Setenv("MAPING_MAX_BODY_BYTES", "0")
		assert.Equal(t, int64(defaultMaxBodyCeiling), maxBodyCeiling(testLogger()))
	})
}

func TestSecureFromBaseURL(t *testing.T) {
	assert.True(t, secureFromBaseURL("https://maping.example.com"))
	assert.False(t, secureFromBaseURL("http://localhost:8080"))
	assert.False(t, secureFromBaseURL(""), "empty base URL -> not secure (local http dev)")
}

func TestOIDCConfigFromEnv(t *testing.T) {
	t.Setenv("MAPING_OIDC_GITHUB_CLIENT_ID", "gh-id")
	t.Setenv("MAPING_OIDC_GITHUB_CLIENT_SECRET", "gh-secret")
	t.Setenv("MAPING_OIDC_GOOGLE_CLIENT_ID", "goog-id")
	t.Setenv("MAPING_OIDC_GOOGLE_CLIENT_SECRET", "goog-secret")

	cfg := oidcConfigFromEnv("https://maping.example.com")
	assert.Equal(t, "gh-id", cfg.GitHubClientID)
	assert.Equal(t, "gh-secret", cfg.GitHubClientSecret)
	assert.Equal(t, "goog-id", cfg.GoogleClientID)
	assert.Equal(t, "goog-secret", cfg.GoogleClientSecret)
	assert.Equal(t, "https://maping.example.com", cfg.BaseURL)
}
