package app

import (
	"crypto/rand"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/arhuman/maping/server/internal/auth"
)

// sessionKey reads MAPING_SESSION_KEY (>= 32 bytes required). When it is unset,
// behavior depends on requireStable: a production deployment (https base URL)
// refuses to start rather than silently minting an ephemeral key that would drop
// every session on the next restart; local http dev generates the ephemeral key
// and warns. The key is never logged.
func sessionKey(requireStable bool, log *slog.Logger) ([]byte, error) {
	if v := os.Getenv("MAPING_SESSION_KEY"); v != "" {
		if len(v) < 32 {
			return nil, fmt.Errorf("MAPING_SESSION_KEY must be >= 32 bytes, got %d", len(v))
		}
		return []byte(v), nil
	}
	if requireStable {
		return nil, fmt.Errorf("MAPING_SESSION_KEY is required for an https deployment: an ephemeral key would drop every session on restart")
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate session key: %w", err)
	}
	log.Warn("MAPING_SESSION_KEY unset: generated an ephemeral session key; sessions will not survive a restart")
	return key, nil
}

// maxBodyCeiling resolves the absolute pre-auth HTTP body cap:
// defaultMaxBodyCeiling, overridable via MAPING_MAX_BODY_BYTES. This is the hard
// memory-safety bound (tenant unknown at the HTTP layer); the per-tenant
// max_payload_bytes is the logical bound enforced after auth in ingest.Upload.
// An invalid or non-positive override is ignored with a warning, keeping the
// safe default.
func maxBodyCeiling(log *slog.Logger) int64 {
	v := os.Getenv("MAPING_MAX_BODY_BYTES")
	if v == "" {
		return defaultMaxBodyCeiling
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		log.Warn("invalid MAPING_MAX_BODY_BYTES, using default ceiling",
			slog.String("value", v), slog.Int64("default", defaultMaxBodyCeiling))
		return defaultMaxBodyCeiling
	}
	return n
}

// oidcConfigFromEnv reads the per-provider OAuth credentials from the environment
// and pairs them with the deployment base URL (used to build each provider's
// redirect URI). Absent credentials leave a provider disabled — auth.New turns
// dev-login on only when no real provider is configured.
func oidcConfigFromEnv(baseURL string) auth.ProviderConfig {
	return auth.ProviderConfig{
		GitHubClientID:     os.Getenv("MAPING_OIDC_GITHUB_CLIENT_ID"),
		GitHubClientSecret: os.Getenv("MAPING_OIDC_GITHUB_CLIENT_SECRET"),
		GoogleClientID:     os.Getenv("MAPING_OIDC_GOOGLE_CLIENT_ID"),
		GoogleClientSecret: os.Getenv("MAPING_OIDC_GOOGLE_CLIENT_SECRET"),
		BaseURL:            baseURL,
	}
}

// secureFromBaseURL reports whether cookies should carry the Secure flag: only
// over an https base URL, so local http dev still works.
func secureFromBaseURL(baseURL string) bool {
	return strings.HasPrefix(baseURL, "https://")
}
