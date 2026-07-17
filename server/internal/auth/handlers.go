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

// loginPage is the two-column sign-in page (handoff design, mapi-ng-landing-page):
// a left form panel with the enabled providers + optional dev-login, and a right
// value panel explaining what happens on first sign-in. Provider buttons and the
// dev-login form are conditional so the page reflects the deployment's real auth
// configuration (GitHub/Google only when configured; dev-login only when enabled).
var loginPage = template.Must(template.New("login").Parse(`<!doctype html>
<html lang="en"><head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>mAPI-ng — sign in</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Hanken+Grotesk:wght@400;500;600;700;800&family=JetBrains+Mono:wght@400;500;600;700&display=swap" rel="stylesheet">
<style>
  *{box-sizing:border-box;}
  html,body{margin:0;padding:0;height:100%;}
  body{background:#0A0C0F;color:#E8EDF3;font-family:'Hanken Grotesk',system-ui,sans-serif;-webkit-font-smoothing:antialiased;}
  a{color:inherit;text-decoration:none;}
  a:hover{color:#B4F14A;}
  @media (max-width:820px){.mp-grid{grid-template-columns:1fr !important;}.mp-value{display:none !important;}}
</style></head>
<body>
<div class="mp-grid" style="min-height:100vh;display:grid;grid-template-columns:1.05fr .95fr;">

  <div style="display:flex;flex-direction:column;padding:34px 44px;background:radial-gradient(700px 420px at 20% -10%,rgba(180,241,74,.06),transparent 60%);">
    <a href="/" style="display:flex;align-items:center;gap:11px;align-self:flex-start;">
      <div style="width:34px;height:34px;display:grid;place-items:center;background:#141A22;border:1px solid rgba(255,255,255,.08);border-radius:9px;">
        <svg width="19" height="19" viewBox="0 0 24 24" fill="none"><path d="M2 15 L6 15 L8 6 L11 20 L14 3 L16 15 L22 15" stroke="#B4F14A" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" fill="none"></path></svg>
      </div>
      <div style="line-height:1;">
        <div style="font-weight:800;font-size:17px;letter-spacing:-.3px;">m<span style="color:#B4F14A;">API</span>ng</div>
        <div style="font:500 9px/1 'JetBrains Mono',monospace;color:#69727F;letter-spacing:1.5px;margin-top:3px;">MONITORED&nbsp;API · NG</div>
      </div>
    </a>

    <div style="flex:1;display:flex;flex-direction:column;justify-content:center;max-width:392px;width:100%;margin:0 auto;padding:40px 0;">
      <h1 style="margin:20px 0 8px;font-size:32px;font-weight:800;letter-spacing:-.9px;">Sign in to mAPI-ng</h1>
      <p style="margin:0 0 26px;font-size:15px;line-height:1.55;color:#9AA4B2;">One account for your org's dashboard and ingest keys. New here? The same button gets you started.</p>

      <div style="display:flex;flex-direction:column;gap:11px;">
        {{if .GitHub}}<a href="/auth/github/start{{if ne .Next "/"}}?next={{.Next}}{{end}}" style="display:flex;align-items:center;justify-content:center;gap:11px;padding:13px 16px;background:#181F28;border:1px solid rgba(255,255,255,.1);border-radius:11px;font:700 14.5px 'Hanken Grotesk',sans-serif;color:#E8EDF3;">
          <svg width="19" height="19" viewBox="0 0 24 24" fill="#E8EDF3"><path d="M12 .5C5.37.5 0 5.87 0 12.5c0 5.3 3.44 9.8 8.21 11.39.6.11.82-.26.82-.58l-.01-2.05c-3.34.73-4.04-1.61-4.04-1.61-.55-1.39-1.34-1.76-1.34-1.76-1.09-.75.08-.73.08-.73 1.2.09 1.84 1.24 1.84 1.24 1.07 1.84 2.81 1.31 3.5 1 .11-.78.42-1.31.76-1.61-2.67-.3-5.47-1.33-5.47-5.93 0-1.31.47-2.38 1.24-3.22-.12-.31-.54-1.52.12-3.18 0 0 1.01-.32 3.3 1.23a11.5 11.5 0 0 1 6 0c2.29-1.55 3.3-1.23 3.3-1.23.66 1.66.24 2.87.12 3.18.77.84 1.23 1.91 1.23 3.22 0 4.61-2.81 5.62-5.49 5.92.43.37.81 1.1.81 2.22l-.01 3.29c0 .32.22.7.83.58C20.56 22.29 24 17.8 24 12.5 24 5.87 18.63.5 12 .5Z"></path></svg>
          Continue with GitHub
        </a>{{end}}
        {{if .Google}}<a href="/auth/google/start{{if ne .Next "/"}}?next={{.Next}}{{end}}" style="display:flex;align-items:center;justify-content:center;gap:11px;padding:13px 16px;background:#181F28;border:1px solid rgba(255,255,255,.1);border-radius:11px;font:700 14.5px 'Hanken Grotesk',sans-serif;color:#E8EDF3;">
          <svg width="18" height="18" viewBox="0 0 24 24"><path fill="#4285F4" d="M23.52 12.27c0-.82-.07-1.6-.2-2.36H12v4.47h6.47a5.53 5.53 0 0 1-2.4 3.63v3h3.88c2.27-2.09 3.57-5.17 3.57-8.74Z"></path><path fill="#34A853" d="M12 24c3.24 0 5.96-1.08 7.95-2.91l-3.88-3c-1.08.72-2.45 1.15-4.07 1.15-3.13 0-5.78-2.11-6.73-4.96H1.28v3.09A12 12 0 0 0 12 24Z"></path><path fill="#FBBC05" d="M5.27 14.28a7.2 7.2 0 0 1 0-4.56V6.63H1.28a12 12 0 0 0 0 10.74l3.99-3.09Z"></path><path fill="#EA4335" d="M12 4.75c1.77 0 3.35.61 4.6 1.8l3.44-3.44A11.98 11.98 0 0 0 12 0 12 12 0 0 0 1.28 6.63l3.99 3.09C6.22 6.86 8.87 4.75 12 4.75Z"></path></svg>
          Continue with Google
        </a>{{end}}
      </div>

      {{if and (or .GitHub .Google) .DevLogin}}
      <div style="display:flex;align-items:center;gap:14px;margin:22px 0;">
        <div style="flex:1;height:1px;background:rgba(255,255,255,.08);"></div>
        <span style="font:600 10px 'JetBrains Mono',monospace;color:#69727F;letter-spacing:.8px;">OR</span>
        <div style="flex:1;height:1px;background:rgba(255,255,255,.08);"></div>
      </div>
      {{end}}

      {{if .DevLogin}}
      <form method="post" action="/auth/dev/login" style="margin-top:{{if or .GitHub .Google}}0{{else}}22px{{end}};">
        <input type="hidden" name="next" value="{{.Next}}">
        <button type="submit" style="width:100%;display:flex;align-items:center;justify-content:center;gap:9px;padding:13px 16px;background:transparent;border:1px dashed rgba(255,255,255,.14);border-radius:11px;font:600 13.5px 'JetBrains Mono',monospace;color:#9AA4B2;cursor:pointer;">
          <span style="color:#B4F14A;">›_</span> Dev login (admin)
        </button>
      </form>
      <p style="margin:11px 2px 0;font:500 11px/1.5 'JetBrains Mono',monospace;color:#69727F;">Shown only when no OIDC provider is configured — <code>MAPING_POSTGRES_DSN</code> set, no GitHub/Google credentials.</p>
      {{end}}

      <p style="margin:28px 0 0;font-size:13px;line-height:1.6;color:#69727F;">By continuing you agree to the Terms and Privacy Policy. First sign-in issues your org's first ingest key and reveals it once.</p>
    </div>

    <div style="display:flex;align-items:center;gap:16px;font:500 12px 'JetBrains Mono',monospace;color:#69727F;">
      <a href="/">← Back to home</a>
    </div>
  </div>

  <div class="mp-value" style="position:relative;background:linear-gradient(180deg,#0C0F14,#0A0C0F);border-left:1px solid rgba(255,255,255,.08);display:flex;flex-direction:column;justify-content:center;padding:48px;overflow:hidden;">
    <div style="position:absolute;inset:0;background:radial-gradient(700px 420px at 80% 0%,rgba(180,241,74,.07),transparent 60%);"></div>
    <div style="position:relative;max-width:420px;">
      <div style="font:600 11px 'JetBrains Mono',monospace;color:#B4F14A;letter-spacing:1.4px;">WHAT HAPPENS NEXT</div>
      <h2 style="margin:14px 0 26px;font-size:28px;font-weight:800;letter-spacing:-.7px;line-height:1.15;">Sign in and your first key is waiting.</h2>
      <div style="display:flex;flex-direction:column;gap:2px;">
        <div style="display:flex;gap:14px;">
          <div style="display:flex;flex-direction:column;align-items:center;">
            <div style="width:30px;height:30px;border-radius:8px;background:rgba(180,241,74,.1);border:1px solid rgba(180,241,74,.24);display:grid;place-items:center;font:800 12px 'JetBrains Mono',monospace;color:#B4F14A;flex:none;">1</div>
            <div style="width:1px;flex:1;background:rgba(255,255,255,.09);margin:4px 0;"></div>
          </div>
          <div style="padding-bottom:22px;">
            <div style="font-size:15px;font-weight:700;letter-spacing:-.2px;">Authenticate</div>
            <div style="margin-top:4px;font-size:13.5px;line-height:1.5;color:#9AA4B2;">GitHub or Google via OIDC. We create your org on first sign-in.</div>
          </div>
        </div>
        <div style="display:flex;gap:14px;">
          <div style="display:flex;flex-direction:column;align-items:center;">
            <div style="width:30px;height:30px;border-radius:8px;background:rgba(180,241,74,.1);border:1px solid rgba(180,241,74,.24);display:grid;place-items:center;font:800 12px 'JetBrains Mono',monospace;color:#B4F14A;flex:none;">2</div>
            <div style="width:1px;flex:1;background:rgba(255,255,255,.09);margin:4px 0;"></div>
          </div>
          <div style="padding-bottom:22px;">
            <div style="font-size:15px;font-weight:700;letter-spacing:-.2px;">Get your ingest key</div>
            <div style="margin-top:4px;font-size:13.5px;line-height:1.5;color:#9AA4B2;">The org's first key is auto-issued and shown once — it carries the secret and the collector endpoint.</div>
          </div>
        </div>
        <div style="display:flex;gap:14px;">
          <div style="display:flex;flex-direction:column;align-items:center;">
            <div style="width:30px;height:30px;border-radius:8px;background:rgba(180,241,74,.1);border:1px solid rgba(180,241,74,.24);display:grid;place-items:center;font:800 12px 'JetBrains Mono',monospace;color:#B4F14A;flex:none;">3</div>
          </div>
          <div style="padding-bottom:22px;">
            <div style="font-size:15px;font-weight:700;letter-spacing:-.2px;">Set MAPING_KEY &amp; ship</div>
            <div style="margin-top:4px;font-size:13.5px;line-height:1.5;color:#9AA4B2;">RED metrics land on your dashboard after the first ~10s flush.</div>
          </div>
        </div>
      </div>

      <div style="margin-top:8px;background:#10141A;border:1px solid rgba(180,241,74,.3);border-radius:12px;padding:15px 17px;">
        <div style="font:600 10px 'JetBrains Mono',monospace;color:#69727F;letter-spacing:.6px;margin-bottom:8px;">YOUR INGEST KEY, REVEALED ONCE</div>
        <code style="display:block;font:600 12.5px 'JetBrains Mono',monospace;color:#B4F14A;word-break:break-all;">export MAPING_KEY=<span style="color:#E8EDF3;">mk_live_7f3a…c091</span></code>
      </div>
    </div>
  </div>
</div>
</body>
</html>`))

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
<a class="go" href="{{.Next}}">Continue to dashboard →</a>
</div>
<script src="/assets/copy.js" defer></script>
</body></html>`))

// handleLoginPage renders the sign-in page. The provider buttons and dev-login
// form are gated on the deployment's real configuration, so the page never offers
// an auth path the server cannot honor.
func (a *Auth) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	_, github := a.providers[providerGitHub]
	_, google := a.providers[providerGoogle]
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Same JS-budget CSP as the interstitial: no scripts, inline styles, and the
	// Google Fonts stylesheet/fonts allowlisted.
	w.Header().Set("Content-Security-Policy", interstitialCSP)
	if err := loginPage.Execute(w, struct {
		GitHub   bool
		Google   bool
		DevLogin bool
		Next     string
	}{GitHub: github, Google: google, DevLogin: a.devLogin, Next: safeNext(r.URL.Query().Get("next"))}); err != nil {
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
	// the provider that started the flow. A sanitized post-login redirect target
	// rides in the same signed cookie (never sent to the provider).
	a.setStateCookie(w, string(name)+"|"+state+"|"+safeNext(r.URL.Query().Get("next")))
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

	id, next, ok := a.resolveIdentity(w, r, name, p)
	if !ok {
		return
	}

	// A post-auth hook (e.g. the invitation flow) may bind this identity to an
	// out-of-band context and fully handle the response; when it does, we stop.
	// A nil hook (no such feature wired) means plain first-login only.
	if a.postAuth != nil && a.postAuth.Handle(a, w, r, id.Subject, id.Email) {
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
		a.renderKeyInterstitial(w, r, orgID, next)
		return
	}
	http.Redirect(w, r, safeNext(next), http.StatusSeeOther)
}

// resolveIdentity validates the OAuth CSRF state, exchanges the code, and fetches
// the verified provider identity. On any failure it writes the appropriate error
// response and returns ok=false; the caller simply returns. Token/provider detail
// is never leaked to the client.
func (a *Auth) resolveIdentity(w http.ResponseWriter, r *http.Request, name providerName, p provider) (id identity, next string, ok bool) {
	wantProvider, wantState, next, ok := a.readStateCookie(r)
	if !ok || wantProvider != string(name) || wantState == "" || r.URL.Query().Get("state") != wantState {
		http.Error(w, "invalid oauth state", http.StatusBadRequest)
		return identity{}, "", false
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return identity{}, "", false
	}
	tok, err := p.config.Exchange(a.oauthClientCtx(r.Context()), code)
	if err != nil {
		a.log.Warn("auth: token exchange failed", errAttr(err))
		http.Error(w, "authentication failed", http.StatusBadGateway)
		return identity{}, "", false
	}
	id, err = a.userinfo(r.Context(), name, p.config, tok)
	if err != nil {
		a.log.Warn("auth: userinfo failed", errAttr(err))
		http.Error(w, "authentication failed", http.StatusBadGateway)
		return identity{}, "", false
	}
	return id, next, true
}

// renderKeyInterstitial mints the org's first ingest key and shows the full token
// exactly once, wrapped with the deployment origin so a single MAPING_KEY carries
// both the credential and the collector endpoint. Issuing is best-effort: a
// failure logs and falls through to the dashboard rather than stranding the user
// (they can mint a key on the Setup page).
func (a *Auth) renderKeyInterstitial(w http.ResponseWriter, r *http.Request, orgID, next string) {
	next = safeNext(next)
	secret, err := a.store.IssueKey(r.Context(), orgID, "default")
	if err != nil {
		a.log.Error("auth: issue first key", errAttr(err))
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	tok := token.Encode(a.baseURL, secret)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Same JS-budget CSP as the dashboard: the interstitial's copy button is
	// driven by /assets/copy.js, the only script allowed to run.
	w.Header().Set("Content-Security-Policy", interstitialCSP)
	if err := interstitialPage.Execute(w, struct{ Token, Next string }{tok, next}); err != nil {
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
	http.Redirect(w, r, safeNext(r.FormValue("next")), http.StatusSeeOther)
}

// handleLogout clears the session cookie and redirects to /login.
func (a *Auth) handleLogout(w http.ResponseWriter, r *http.Request) {
	a.clearCookie(w, sessionCookie)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
