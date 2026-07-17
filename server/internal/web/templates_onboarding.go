package web

// tplOnboardingHTML holds the onboarding (get-started) page and the Setup keys
// page, which share the handshake timeline partial markup.
const tplOnboardingHTML = `
{{define "handshake-stepper"}}<div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:6px;"><span style="font:700 11px var(--mono);color:var(--txt-3);letter-spacing:1px;">HANDSHAKE</span></div>
<div style="font-size:12px;color:var(--txt-3);margin-bottom:18px;">Live status from your first recorder.</div>
<div style="display:flex;flex-direction:column;gap:2px;">
  {{range .Steps}}
  <div style="display:flex;align-items:flex-start;gap:13px;padding:11px 4px;">
    <div style="display:flex;flex-direction:column;align-items:center;">
      <div style="width:22px;height:22px;border-radius:50%;display:grid;place-items:center;font:700 11px var(--mono);
        {{if eq .DotClass "dot-done"}}background:var(--accent);border:1.5px solid var(--accent);color:#0A0C0F;
        {{else if eq .DotClass "dot-current"}}background:rgba(180,241,74,.12);border:1.5px solid var(--accent);color:var(--accent);
        {{else}}background:var(--panel-3);border:1.5px solid var(--line);color:var(--txt-3);{{end}}">{{.Icon}}</div>
      {{if .Connector}}<div style="width:1.5px;height:22px;background:var(--line);margin-top:2px;"></div>{{end}}
    </div>
    <div style="padding-top:1px;"><div style="font-size:13px;font-weight:600;" class="{{.LabelClass}}">{{.Label}}</div><div style="font:500 11px var(--mono);color:var(--txt-3);margin-top:2px;">{{.Sub}}</div></div>
  </div>
  {{end}}
</div>
{{if .Connected}}
<div style="margin-top:16px;padding-top:14px;border-top:1px solid var(--line);">
  <div style="font:600 10px var(--mono);color:var(--txt-3);letter-spacing:.8px;margin-bottom:8px;">CONNECTED</div>
  {{range .Connected}}<div style="font:500 11px var(--mono);color:var(--txt-2);margin-bottom:4px;">{{.Service}}{{if .Instance}} · {{.Instance}}{{end}}</div>{{end}}
</div>
{{end}}{{end}}

{{define "onboarding"}}<!doctype html>
<html lang="en"><head>{{template "head"}}{{if .Refresh}}<noscript><meta http-equiv="refresh" content="10"></noscript>{{end}}<title>mAPI-ng — get started</title></head>
<body><div class="app">{{template "sidebar" .Shell}}<main class="main">{{template "topbar" .Shell}}
<div class="scrollbody"><div style="max-width:1060px;margin:0 auto;" class="fade">
  {{if .Frozen}}<div class="warnbar"><span style="font-size:15px;">⚠</span><span class="c-txt2"><b class="c-warn">Cardinality frozen.</b> New series are rejected for this tenant.</span></div>{{end}}
  <div style="display:inline-flex;align-items:center;gap:8px;font:600 11px var(--mono);color:var(--accent);background:rgba(180,241,74,.09);border:1px solid rgba(180,241,74,.22);padding:6px 12px;border-radius:20px;letter-spacing:.5px;">◗ ZERO-CONFIG SETUP</div>
  <h2 style="font-size:38px;font-weight:800;letter-spacing:-1px;margin:20px 0 10px;max-width:640px;line-height:1.05;">One env var. Live in <span class="c-accent">seconds</span>.</h2>
  <p style="font-size:15.5px;color:var(--txt-2);max-width:560px;margin:0 0 30px;line-height:1.6;">No Prometheus. No Grafana. No YAML. Add the middleware, set <span style="font:600 13px var(--mono);color:var(--txt);">MAPING_KEY</span>, and RED metrics for every Go endpoint start flowing.</p>
  <div style="display:grid;grid-template-columns:1.15fr .85fr;gap:22px;align-items:start;min-width:0;">
    <div style="display:flex;flex-direction:column;gap:16px;min-width:0;">
      <div class="panel" style="overflow:hidden;">
        <div style="display:flex;align-items:center;gap:8px;padding:11px 15px;border-bottom:1px solid var(--line);background:var(--panel-2);"><span style="font:700 11px var(--mono);color:var(--accent);">1</span><span style="font-size:12.5px;font-weight:600;">Wire the middleware</span><span style="margin-left:auto;font:500 10.5px var(--mono);color:var(--txt-3);">main.go</span></div>
        <pre>rec := maping.NewRecorder(
    maping.WithService("checkout-api"),
)
r.Use(mapinggin.MiddlewareWithRecorder(rec)) // above Recovery
r.Use(gin.Recovery())</pre>
      </div>
      <div class="panel" style="overflow:hidden;">
        <div style="display:flex;align-items:center;gap:8px;padding:11px 15px;border-bottom:1px solid var(--line);background:var(--panel-2);"><span style="font:700 11px var(--mono);color:var(--accent);">2</span><span style="font-size:12.5px;font-weight:600;">Activate</span></div>
        <pre style="color:var(--accent);">export MAPING_KEY=mk_live_9f2c····a91f</pre>
      </div>
      <div style="display:flex;align-items:center;gap:10px;font-size:12.5px;color:var(--txt-3);padding:2px 4px;"><span class="c-txt2">◇</span> Absent key ⇒ the middleware is a no-op. Shipping mAPI-ng is always safe.</div>
    </div>
    <div class="panel" style="padding:20px;">
      <div id="handshake" data-complete="{{if .Handshake.Complete}}true{{else}}false{{end}}">{{template "handshake-stepper" .Handshake}}</div>
    </div>
  </div>
</div></div>
</main></div>
<script src="/assets/handshake.js" defer></script>
</body></html>{{end}}

{{define "setup"}}<!doctype html>
<html lang="en"><head>{{template "head"}}{{if .Refresh}}<noscript><meta http-equiv="refresh" content="10"></noscript>{{end}}<title>mAPI-ng — setup</title></head>
<body><div class="app">{{template "sidebar" .Shell}}<main class="main">{{template "topbar" .Shell}}
<div class="scrollbody"><div style="max-width:1060px;margin:0 auto;" class="fade">
  {{if .Frozen}}<div class="warnbar"><span style="font-size:15px;">⚠</span><span class="c-txt2"><b class="c-warn">Cardinality frozen.</b> New series are rejected for this tenant.</span></div>{{end}}
  {{if .NewToken}}
  <div class="panel" style="padding:20px 22px;margin-bottom:22px;border-color:rgba(180,241,74,.3);">
    <div style="display:flex;align-items:center;gap:8px;margin-bottom:12px;"><span style="font:600 12px var(--mono);color:var(--warn);">⚠ COPY IT NOW — YOU WON'T SEE IT AGAIN</span><button type="button" data-copy="mp-newkey" style="margin-left:auto;padding:5px 10px;border:1px solid var(--line);border-radius:7px;background:var(--panel-3);color:var(--txt-2);font:600 11px var(--mono);cursor:pointer;">⧉ copy</button></div>
    <code id="mp-newkey" style="user-select:all;display:block;padding:14px 15px;background:var(--bg);border:1px solid rgba(180,241,74,.3);border-radius:10px;color:var(--accent);font:600 12.5px/1.5 var(--mono);word-break:break-all;">export MAPING_KEY={{.NewToken}}</code>
  </div>
  {{end}}
  <div style="display:grid;grid-template-columns:1.1fr .9fr;gap:22px;align-items:start;min-width:0;">
    <div style="display:flex;flex-direction:column;gap:16px;min-width:0;">
      {{if .ShowKeys}}
      <div class="panel" style="overflow:hidden;">
        <div class="thead" style="grid-template-columns:1.4fr 1.1fr .9fr .7fr;"><span>LABEL</span><span>KEY</span><span>CREATED</span><span style="text-align:right;">ACTION</span></div>
        {{range .Keys}}
        <div class="trow" style="grid-template-columns:1.4fr 1.1fr .9fr .7fr;cursor:default;">
          <div style="font-size:13px;font-weight:600;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;">{{.Label}}</div>
          <div class="mono" style="color:var(--txt-2);font-size:12.5px;">mk_live_{{.Masked}}</div>
          <div class="mono" style="color:var(--txt-3);font-size:12px;">{{.Created}}</div>
          <div style="text-align:right;">
            {{if .Revoked}}<span class="mono" style="color:var(--txt-3);font-size:11px;">revoked</span>
            {{else}}<form method="post" action="/setup/keys/{{.ID}}/revoke" style="display:inline;"><input type="hidden" name="csrf_token" value="{{$.CSRFToken}}"><button type="submit" style="padding:4px 10px;border:1px solid rgba(255,107,107,.3);border-radius:7px;background:rgba(255,107,107,.08);color:var(--err);font:600 11px var(--mono);cursor:pointer;">revoke</button></form>{{end}}
          </div>
        </div>
        {{else}}
        <div style="padding:22px 20px;color:var(--txt-3);font-size:13px;">No keys yet. Create one to start ingesting.</div>
        {{end}}
      </div>
      <form method="post" action="/setup/keys" class="panel" style="padding:15px 17px;display:flex;gap:10px;align-items:center;">
        <input type="hidden" name="csrf_token" value="{{.CSRFToken}}">
        <input name="label" placeholder="label (e.g. checkout-api)" maxlength="64" autocomplete="off" style="flex:1;padding:9px 12px;background:var(--bg);border:1px solid var(--line);border-radius:9px;color:var(--txt);font:500 12.5px var(--mono);outline:none;">
        <button type="submit" style="padding:9px 16px;border:none;border-radius:9px;background:var(--accent);color:#0A0C0F;font:700 12.5px var(--ui);cursor:pointer;">Create key</button>
      </form>
      <div style="font:500 11px var(--mono);color:var(--txt-3);padding:2px 4px;line-height:1.5;">The full token carries this deployment's collector endpoint, so <span class="c-txt2">MAPING_KEY</span> is the only env var your service needs. The secret is shown once at creation.</div>
      {{else}}
      <div class="panel" style="padding:20px 22px;color:var(--txt-3);font-size:13px;line-height:1.6;">Self-serve key management is available once the control plane is configured. This deployment authenticates ingest with a static <span class="mono c-txt2">dev-key</span>.</div>
      {{end}}
    </div>
    <div class="panel" style="padding:20px;">
      <div id="handshake" data-complete="{{if .Handshake.Complete}}true{{else}}false{{end}}">{{template "handshake-stepper" .Handshake}}</div>
    </div>
  </div>
  {{if .ShowTeam}}
  <div class="panel" style="margin-top:22px;padding:20px 22px;">
    <div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:14px;">
      <span style="font:700 11px var(--mono);color:var(--txt-3);letter-spacing:1px;">TEAM</span>
      <span style="font:600 11px var(--mono);color:var(--txt-2);">{{.SeatUsed}} of {{.SeatLimit}} seats</span>
    </div>
    {{if .InviteURL}}
    <div class="panel" style="padding:14px 15px;margin-bottom:16px;border-color:rgba(180,241,74,.3);">
      <div style="font:600 11px var(--mono);color:var(--warn);margin-bottom:8px;">⚠ SEND THIS LINK — IT WON'T BE SHOWN AGAIN <button type="button" data-copy="mp-invite" style="margin-left:6px;padding:4px 9px;border:1px solid var(--line);border-radius:7px;background:var(--panel-3);color:var(--txt-2);font:600 10.5px var(--mono);cursor:pointer;">⧉ copy</button></div>
      <code id="mp-invite" style="user-select:all;display:block;padding:12px 13px;background:var(--bg);border:1px solid rgba(180,241,74,.3);border-radius:9px;color:var(--accent);font:600 12px/1.5 var(--mono);word-break:break-all;">{{.InviteURL}}</code>
    </div>
    {{end}}
    {{if .TeamError}}<div class="warnbar" style="margin-bottom:14px;"><span style="font-size:14px;">⚠</span><span class="c-txt2">{{.TeamError}}</span></div>{{end}}
    <div class="thead" style="grid-template-columns:1.7fr .7fr .6fr;"><span>MEMBER</span><span>ROLE</span><span style="text-align:right;">ACTION</span></div>
    {{range .Members}}
    <div class="trow" style="grid-template-columns:1.7fr .7fr .6fr;cursor:default;">
      <div style="font-size:13px;font-weight:600;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;">{{.Email}}{{if .IsOwner}} <span class="mono" style="color:var(--txt-3);font-size:10.5px;">· owner</span>{{end}}</div>
      <div class="mono" style="color:var(--txt-2);font-size:12px;">{{.Role}}</div>
      <div style="text-align:right;">
        {{if and $.IsAdmin (not .IsOwner)}}<form method="post" action="/setup/members/{{.ID}}/remove" style="display:inline;"><input type="hidden" name="csrf_token" value="{{$.CSRFToken}}"><button type="submit" style="padding:4px 10px;border:1px solid rgba(255,107,107,.3);border-radius:7px;background:rgba(255,107,107,.08);color:var(--err);font:600 11px var(--mono);cursor:pointer;">remove</button></form>{{end}}
      </div>
    </div>
    {{end}}
    {{range .Invites}}
    <div class="trow" style="grid-template-columns:1.7fr .7fr .6fr;cursor:default;">
      <div style="font-size:13px;color:var(--txt-2);white-space:nowrap;overflow:hidden;text-overflow:ellipsis;">{{.Email}} <span class="mono" style="color:var(--txt-3);font-size:10.5px;">· invited, expires {{.Expires}}</span></div>
      <div class="mono" style="color:var(--txt-3);font-size:12px;">{{.Role}}</div>
      <div style="text-align:right;">
        {{if $.IsAdmin}}<form method="post" action="/setup/invites/{{.ID}}/revoke" style="display:inline;"><input type="hidden" name="csrf_token" value="{{$.CSRFToken}}"><button type="submit" style="padding:4px 10px;border:1px solid var(--line);border-radius:7px;background:var(--panel-3);color:var(--txt-2);font:600 11px var(--mono);cursor:pointer;">cancel</button></form>{{end}}
      </div>
    </div>
    {{end}}
    {{if .IsAdmin}}
    <form method="post" action="/setup/invites" style="margin-top:14px;display:flex;gap:10px;align-items:center;">
      <input type="hidden" name="csrf_token" value="{{.CSRFToken}}">
      <input name="email" type="email" required placeholder="teammate@company.com" maxlength="254" autocomplete="off" style="flex:1;padding:9px 12px;background:var(--bg);border:1px solid var(--line);border-radius:9px;color:var(--txt);font:500 12.5px var(--mono);outline:none;">
      <select name="role" style="padding:9px 12px;background:var(--bg);border:1px solid var(--line);border-radius:9px;color:var(--txt);font:500 12.5px var(--mono);outline:none;"><option value="member">member</option><option value="admin">admin</option></select>
      <button type="submit" style="padding:9px 16px;border:none;border-radius:9px;background:var(--accent);color:#0A0C0F;font:700 12.5px var(--ui);cursor:pointer;">Invite</button>
    </form>
    {{else}}
    <div style="margin-top:12px;font:500 11px var(--mono);color:var(--txt-3);">Only admins can invite or remove members.</div>
    {{end}}
  </div>
  {{end}}
</div></div>
</main></div>
<script src="/assets/copy.js" defer></script>
<script src="/assets/handshake.js" defer></script>
</body></html>{{end}}
`
