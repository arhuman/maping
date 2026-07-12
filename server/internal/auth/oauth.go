package auth

import (
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
	"golang.org/x/oauth2/google"
)

// providerName identifies an OIDC provider in URLs (/auth/{provider}/start) and
// in the oidc_subject prefix ("github:...", "google:...").
type providerName string

const (
	providerGitHub providerName = "github"
	providerGoogle providerName = "google"
)

// provider is one configured OAuth login option: its oauth2 config and the
// scopes to request. userinfo is fetched separately (see userinfo.go).
type provider struct {
	name   providerName
	config *oauth2.Config
}

// buildProvider constructs a provider's oauth2.Config from client credentials
// and the server base URL (for the redirect URI), or returns ok=false when the
// credentials are absent — each provider is independently optional. baseURL is
// e.g. "https://maping.example.com"; the redirect is baseURL + callback path.
func buildProvider(name providerName, clientID, clientSecret, baseURL string) (provider, bool) {
	if clientID == "" || clientSecret == "" {
		return provider{}, false
	}
	cfg := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  strings.TrimRight(baseURL, "/") + "/auth/" + string(name) + "/callback",
	}
	switch name {
	case providerGitHub:
		cfg.Endpoint = github.Endpoint
		// read:user + user:email so the userinfo call can read a verified
		// primary email even when the public profile hides it.
		cfg.Scopes = []string{"read:user", "user:email"}
	case providerGoogle:
		cfg.Endpoint = google.Endpoint
		cfg.Scopes = []string{"openid", "email"}
	default:
		return provider{}, false
	}
	return provider{name: name, config: cfg}, true
}

// ProviderConfig is the raw per-provider credential input read from the
// environment by main and passed to newAuth.
type ProviderConfig struct {
	GitHubClientID     string
	GitHubClientSecret string
	GoogleClientID     string
	GoogleClientSecret string
	BaseURL            string
}

// buildProviders assembles the enabled providers from the credential config,
// keyed by name for callback dispatch. An empty map means no real provider is
// configured (which turns dev-login on — see newAuth).
func buildProviders(pc ProviderConfig) map[providerName]provider {
	out := make(map[providerName]provider, 2)
	if p, ok := buildProvider(providerGitHub, pc.GitHubClientID, pc.GitHubClientSecret, pc.BaseURL); ok {
		out[providerGitHub] = p
	}
	if p, ok := buildProvider(providerGoogle, pc.GoogleClientID, pc.GoogleClientSecret, pc.BaseURL); ok {
		out[providerGoogle] = p
	}
	return out
}
