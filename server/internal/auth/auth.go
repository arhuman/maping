package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// Cookie and session lifetimes.
const (
	// sessionMaxAge is how long a login lasts (matches the session Exp).
	sessionMaxAge = 7 * 24 * time.Hour
	// stateMaxAge bounds the OAuth CSRF state cookie: long enough for a human
	// to complete the provider consent screen, short enough to limit replay.
	stateMaxAge   = 10 * time.Minute
	sessionCookie = "maping_session"
	stateCookie   = "maping_oauth_state"
)

// MemberStore is the control-plane surface the auth layer needs. control.Store
// satisfies it. Keeping it an interface here means auth never imports control
// (auth sits above the control plane in the wiring only through main) and the
// handlers are unit-testable against a fake.
type MemberStore interface {
	// UpsertMemberFromOIDC resolves (creating on first login) the member behind
	// an OIDC identity, returning its org id, member id, role, and whether this
	// call created the member (first login) — which triggers the key interstitial.
	UpsertMemberFromOIDC(ctx context.Context, oidcSubject, email string) (orgID, memberID, role string, isNew bool, err error)
	// DevOrgAdmin returns the seeded dev org's admin member, for dev-login.
	DevOrgAdmin(ctx context.Context, devOrgName string) (orgID, memberID string, err error)
	// IssueKey mints an ingest key for an org and returns its plaintext secret
	// once (only the hash is stored). Used to auto-issue the first key at signup.
	IssueKey(ctx context.Context, orgID, label string) (secret string, err error)
}

// Config bundles the auth layer's dependencies and startup options. main builds
// it from the environment.
type Config struct {
	// Store is the control-plane member store (required).
	Store MemberStore
	// SessionKey signs session cookies (>= 32 bytes required).
	SessionKey []byte
	// Providers holds the per-provider OAuth credentials + base URL.
	Providers ProviderConfig
	// DevOrgName is the seeded dev org used by dev-login (when enabled).
	DevOrgName string
	// Secure sets the Secure cookie flag; main sets it true when the base URL
	// is https.
	Secure bool
	// HTTPClient performs the token exchange and userinfo calls. main supplies
	// one with an explicit timeout.
	HTTPClient *http.Client
	// Logger is the structured logger (defaults to slog.Default()).
	Logger *slog.Logger
}

// Auth is the mAPI-ng dashboard auth layer. It mounts open login routes, verifies
// session cookies in its Middleware, and exposes TenantFromContext for the web
// layer's tenant resolver.
type Auth struct {
	store      MemberStore
	signer     *signer
	providers  map[providerName]provider
	devLogin   bool // true only when no real provider is configured
	devOrgName string
	baseURL    string // deployment origin embedded in auto-issued keys
	secure     bool
	client     *http.Client
	userinfo   userinfoFunc
	log        *slog.Logger
}

// New builds the auth layer. Dev-login is enabled ONLY when no real provider is
// configured, so a production deployment with GitHub/Google never exposes the
// bypass. Returns an error for a too-short session key.
func New(cfg Config) (*Auth, error) {
	sgn, err := newSigner(cfg.SessionKey)
	if err != nil {
		return nil, err
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	providers := buildProviders(cfg.Providers)
	a := &Auth{
		store:      cfg.Store,
		signer:     sgn,
		providers:  providers,
		devLogin:   len(providers) == 0,
		devOrgName: cfg.DevOrgName,
		baseURL:    strings.TrimRight(cfg.Providers.BaseURL, "/"),
		secure:     cfg.Secure,
		client:     client,
		userinfo:   httpUserinfo(client),
		log:        log,
	}
	return a, nil
}

// Enabled reports the active auth configuration: the enabled provider names and
// whether dev-login is on. main logs it at startup so the active mode is clear.
func (a *Auth) Enabled() (providers []string, devLogin bool) {
	for n := range a.providers {
		providers = append(providers, string(n))
	}
	return providers, a.devLogin
}

// randomToken returns n cryptographically-random bytes base64url-encoded, for
// OAuth state. It panics only if the OS RNG fails, which is unrecoverable.
func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("auth: crypto/rand: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// setSessionCookie signs sess and writes it as the session cookie.
func (a *Auth) setSessionCookie(w http.ResponseWriter, sess Session) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    a.signer.encode(sess),
		Path:     "/",
		HttpOnly: true,
		Secure:   a.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionMaxAge / time.Second),
	})
}

// clearCookie expires a cookie by name.
func (a *Auth) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   a.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// newSession builds a session for a resolved member with the standard lifetime.
func newSession(orgID, memberID, role string) Session {
	return Session{
		OrgID:    orgID,
		MemberID: memberID,
		Role:     role,
		Exp:      time.Now().Add(sessionMaxAge),
	}
}

// oauthClientCtx returns a context carrying the auth HTTP client so oauth2's
// token exchange reuses our timeout-bounded client instead of http.DefaultClient.
func (a *Auth) oauthClientCtx(ctx context.Context) context.Context {
	return context.WithValue(ctx, oauth2.HTTPClient, a.client)
}

// providerFromPath normalizes the {provider} path segment.
func providerFromPath(v string) providerName {
	return providerName(strings.ToLower(v))
}

// setStateCookie stores the OAuth CSRF state in a short-lived signed HttpOnly
// cookie. The value is signed with the session key (reusing the signer's MAC
// via an ephemeral Session-shaped payload would be awkward, so it signs the raw
// payload directly) so a client cannot forge or tamper with the stored state.
func (a *Auth) setStateCookie(w http.ResponseWriter, payload string) {
	p := []byte(payload)
	signed := base64.RawURLEncoding.EncodeToString(p) + "." +
		base64.RawURLEncoding.EncodeToString(a.signer.mac(p))
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookie,
		Value:    signed,
		Path:     "/",
		HttpOnly: true,
		Secure:   a.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(stateMaxAge / time.Second),
	})
}

// readStateCookie verifies and splits the state cookie into (provider, state).
// ok=false when the cookie is absent, malformed, or its MAC does not verify.
func (a *Auth) readStateCookie(r *http.Request) (provider, state string, ok bool) {
	c, err := r.Cookie(stateCookie)
	if err != nil {
		return "", "", false
	}
	dot := strings.IndexByte(c.Value, '.')
	if dot < 0 {
		return "", "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(c.Value[:dot])
	if err != nil {
		return "", "", false
	}
	gotMAC, err := base64.RawURLEncoding.DecodeString(c.Value[dot+1:])
	if err != nil {
		return "", "", false
	}
	if !hmac.Equal(gotMAC, a.signer.mac(payload)) {
		return "", "", false
	}
	parts := strings.SplitN(string(payload), "|", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// errAttr wraps an error for structured logging without leaking it into a
// message string.
func errAttr(err error) slog.Attr { return slog.Any("err", err) }
