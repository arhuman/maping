package auth

import (
	"html/template"
	"net/http"
	"time"

	"golang.org/x/oauth2"

	"github.com/arhuman/maping/proto/token"
)

// timeNow is the clock, overridable in tests.
var timeNow = time.Now

// interstitialCSP pins the JS budget on the key-reveal page: script-src 'self'
// so the only script that runs is the self-hosted copy helper. Inline styles and
// the Google Fonts stylesheet/fonts are allowlisted, matching the dashboard's
// policy (web.contentSecurityPolicy) without coupling auth to the web package.
const interstitialCSP = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; " +
	"font-src https://fonts.gstatic.com; " +
	"img-src 'self' data:; " +
	"base-uri 'none'; frame-ancestors 'none'"

// Register mounts the OPEN auth routes on mux. None of these sit behind the
// session gate (a logged-out user must reach the login flow). main mounts the
// session-gated dashboard separately at "/".
func (a *Auth) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /login", a.handleLoginPage)
	mux.HandleFunc("GET /auth/{provider}/start", a.handleStart)
	mux.HandleFunc("GET /auth/{provider}/callback", a.handleCallback)
	mux.HandleFunc("GET /logout", a.handleLogout)
	mux.HandleFunc("POST /logout", a.handleLogout)
	if a.devLogin {
		mux.HandleFunc("POST /auth/dev/login", a.handleDevLogin)
	}
}

// loginPage is the small login template: one button per enabled provider, and a
// dev-login form when dev-login is on.
var loginPage = template.Must(template.New("login").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>mAPI-ng — sign in</title></head>
<body>
<h1>Sign in to mAPI-ng</h1>
{{range .Providers}}<p><a href="/auth/{{.}}/start">Sign in with {{.}}</a></p>{{end}}
{{if .DevLogin}}<form method="post" action="/auth/dev/login"><button type="submit">Dev login (admin)</button></form>{{end}}
</body></html>`))

// interstitialPage reveals a newly-issued ingest key exactly once at signup. The
// token is shown in a select-all block (works with no JS); the copy button is
// activated by the shared copy helper (/assets/copy.js), the dashboard's only
// script. The full token is a single MAPING_KEY that carries both the secret and
// the collector endpoint.
var interstitialPage = template.Must(template.New("interstitial").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>mAPI-ng — your ingest key</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link href="https://fonts.googleapis.com/css2?family=Hanken+Grotesk:wght@400;600;700;800&family=JetBrains+Mono:wght@500;600&display=swap" rel="stylesheet">
<style>
body{margin:0;min-height:100vh;display:grid;place-items:center;background:#0A0C0F;color:#E8EDF3;font-family:'Hanken Grotesk',system-ui,sans-serif;}
.card{max-width:620px;margin:24px;padding:32px;background:#10141A;border:1px solid rgba(255,255,255,.08);border-radius:16px;}
h1{font-size:24px;font-weight:800;letter-spacing:-.4px;margin:0 0 6px;}
.sub{color:#9AA4B2;font-size:14px;line-height:1.5;margin:0 0 22px;}
.warn{color:#F5C451;font:600 12px 'JetBrains Mono',monospace;margin:0 0 10px;}
.key{user-select:all;display:block;padding:16px;background:#0A0C0F;border:1px solid rgba(180,241,74,.3);border-radius:10px;color:#B4F14A;font:600 13px/1.5 'JetBrains Mono',monospace;word-break:break-all;}
.hint{color:#69727F;font:500 11.5px 'JetBrains Mono',monospace;margin:8px 2px 24px;}
.copy{margin-left:8px;padding:5px 10px;border:1px solid rgba(255,255,255,.08);border-radius:7px;background:#181F28;color:#9AA4B2;font:600 11px 'JetBrains Mono',monospace;cursor:pointer;}
.go{display:inline-block;padding:12px 20px;border-radius:10px;background:#B4F14A;color:#0A0C0F;font-weight:700;font-size:14px;}
code{color:#E8EDF3;font:600 12px 'JetBrains Mono',monospace;}
</style></head>
<body><div class="card">
<h1>You're in. Here's your ingest key.</h1>
<p class="sub">Set it as <code>MAPING_KEY</code> in your Go service — it carries both your key and this deployment's collector endpoint, so no other config is needed.</p>
<p class="warn">⚠ COPY IT NOW — YOU WON'T SEE IT AGAIN <button class="copy" data-copy="mp-key" type="button">⧉ copy</button></p>
<code class="key" id="mp-key">export MAPING_KEY={{.Token}}</code>
<p class="hint">Lost it? Mint a new one anytime on the Setup page.</p>
<a class="go" href="/">Continue to dashboard →</a>
</div>
<script src="/assets/copy.js" defer></script>
</body></html>`))

// handleLoginPage renders the provider buttons (and dev-login when enabled).
func (a *Auth) handleLoginPage(w http.ResponseWriter, _ *http.Request) {
	names := make([]providerName, 0, len(a.providers))
	for n := range a.providers {
		names = append(names, n)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := loginPage.Execute(w, struct {
		Providers []providerName
		DevLogin  bool
	}{Providers: names, DevLogin: a.devLogin}); err != nil {
		a.log.Error("auth: render login", errAttr(err))
	}
}

// handleStart begins the OAuth flow: it generates a CSRF state, stores it in a
// short-lived signed cookie alongside the chosen provider, and redirects to the
// provider's consent screen.
func (a *Auth) handleStart(w http.ResponseWriter, r *http.Request) {
	name := providerFromPath(r.PathValue("provider"))
	p, ok := a.providers[name]
	if !ok {
		http.Error(w, "unknown provider", http.StatusNotFound)
		return
	}
	state := randomToken(32)
	// Bind the state to the provider so the callback can only be replayed for
	// the provider that started the flow.
	a.setStateCookie(w, string(name)+"|"+state)
	http.Redirect(w, r, p.config.AuthCodeURL(state, oauth2.AccessTypeOnline), http.StatusSeeOther)
}

// handleCallback validates the CSRF state, exchanges the code, fetches the
// verified identity, upserts the member, sets the session cookie, and redirects
// to the dashboard. Any failure clears the state cookie and returns an error
// without leaking token or provider detail.
func (a *Auth) handleCallback(w http.ResponseWriter, r *http.Request) {
	name := providerFromPath(r.PathValue("provider"))
	p, ok := a.providers[name]
	if !ok {
		http.Error(w, "unknown provider", http.StatusNotFound)
		return
	}
	defer a.clearCookie(w, stateCookie)

	wantProvider, wantState, ok := a.readStateCookie(r)
	if !ok || wantProvider != string(name) || wantState == "" || r.URL.Query().Get("state") != wantState {
		http.Error(w, "invalid oauth state", http.StatusBadRequest)
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}

	tok, err := p.config.Exchange(a.oauthClientCtx(r.Context()), code)
	if err != nil {
		a.log.Warn("auth: token exchange failed", errAttr(err))
		http.Error(w, "authentication failed", http.StatusBadGateway)
		return
	}

	id, err := a.userinfo(r.Context(), name, p.config, tok)
	if err != nil {
		a.log.Warn("auth: userinfo failed", errAttr(err))
		http.Error(w, "authentication failed", http.StatusBadGateway)
		return
	}

	orgID, memberID, role, isNew, err := a.store.UpsertMemberFromOIDC(r.Context(), id.Subject, id.Email)
	if err != nil {
		a.log.Error("auth: upsert member", errAttr(err))
		http.Error(w, "sign-in failed", http.StatusInternalServerError)
		return
	}

	a.setSessionCookie(w, newSession(orgID, memberID, role))

	// First login: auto-issue the org's first ingest key and reveal it once on an
	// interstitial. No cookie carries the plaintext; the OAuth code is single-use,
	// so this render is inherently reveal-once. Returning users go straight in.
	if isNew {
		a.renderKeyInterstitial(w, r, orgID)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// renderKeyInterstitial mints the org's first ingest key and shows the full token
// exactly once, wrapped with the deployment origin so a single MAPING_KEY carries
// both the credential and the collector endpoint. Issuing is best-effort: a
// failure logs and falls through to the dashboard rather than stranding the user
// (they can mint a key on the Setup page).
func (a *Auth) renderKeyInterstitial(w http.ResponseWriter, r *http.Request, orgID string) {
	secret, err := a.store.IssueKey(r.Context(), orgID, "default")
	if err != nil {
		a.log.Error("auth: issue first key", errAttr(err))
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	tok := token.Encode(a.baseURL, secret)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Same JS-budget CSP as the dashboard: the interstitial's copy button is
	// driven by /assets/copy.js, the only script allowed to run.
	w.Header().Set("Content-Security-Policy", interstitialCSP)
	if err := interstitialPage.Execute(w, struct{ Token string }{tok}); err != nil {
		a.log.Error("auth: render interstitial", errAttr(err))
	}
}

// handleDevLogin starts a session as the seeded dev-org admin. It is only
// registered when dev-login is enabled (no real provider configured).
func (a *Auth) handleDevLogin(w http.ResponseWriter, r *http.Request) {
	orgID, memberID, err := a.store.DevOrgAdmin(r.Context(), a.devOrgName)
	if err != nil {
		a.log.Error("auth: dev login", errAttr(err))
		http.Error(w, "dev login failed", http.StatusInternalServerError)
		return
	}
	a.setSessionCookie(w, newSession(orgID, memberID, "admin"))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleLogout clears the session cookie and redirects to /login.
func (a *Auth) handleLogout(w http.ResponseWriter, r *http.Request) {
	a.clearCookie(w, sessionCookie)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
