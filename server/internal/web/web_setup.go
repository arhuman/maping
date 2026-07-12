package web

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/arhuman/maping/server/internal/tenant"
)

// serveSetup renders the always-reachable Setup page: the live handshake stepper
// and — when a control plane is present — the self-serve ingest-keys panel. Keys
// management is here (not gated on "no data yet") so a running tenant can still
// mint and revoke keys.
func (h *Handler) serveSetup(w http.ResponseWriter, r *http.Request) {
	tid, ok := h.resolveTenant(w, r)
	if !ok {
		return
	}
	winKey := normalizeWindow(r.URL.Query().Get("win"))
	h.renderSetup(w, r, tid, winKey, "")
}

// renderSetup builds the Setup page from the live handshake state plus the keys
// panel. newToken carries the reveal-once plaintext right after a create POST; it
// is empty on a plain GET so the banner shows only once, by construction.
func (h *Handler) renderSetup(w http.ResponseWriter, r *http.Request, tid tenant.ID, winKey, newToken string) {
	var connected []ServiceOnboarding
	if h.onboarding != nil {
		got, err := h.onboarding(r.Context(), tid.String())
		if err != nil {
			h.log.Error("web: onboarding source", slog.Any("err", err))
		} else {
			connected = got
		}
	}
	ob := buildOnboarding(connected, h.frozenFor(tid))

	// While onboarding is incomplete (no summary yet), auto-refresh so the
	// handshake stepper advances live; once data exists, stop refreshing. A
	// has-data check failure defaults to no refresh rather than a hammering loop.
	refresh := false
	if hasData, err := h.q.Tenant(tid).HasAnySummary(r.Context()); err != nil {
		h.log.Error("web: setup has-data", slog.Any("err", err))
	} else {
		refresh = !hasData
	}

	page := setupPage{
		Shell:     h.buildShell(r, "setup", []crumb{{Label: "setup"}}, "Setup", false, winKey),
		Steps:     onboardingStepViews(ob.Steps),
		Connected: ob.Connected,
		Frozen:    ob.Frozen,
		NewToken:  newToken,
		Refresh:   refresh,
	}
	if h.keys != nil {
		page.ShowKeys = true
		page.CSRFToken = h.csrf.issue(tid.String())
		infos, err := h.keys.ListKeys(r.Context(), tid.String())
		if err != nil {
			h.log.Error("web: list keys", slog.Any("err", err))
		} else {
			page.Keys = toKeyRows(infos)
		}
	}
	h.render(w, "setup", page)
}

// serveCreateKey mints a new ingest key and re-renders Setup with the full token
// revealed once. The reveal is on the POST response itself (no redirect): the
// plaintext exists only here and is never stored, so a refresh cannot re-show it
// — it would mint a fresh key instead.
func (h *Handler) serveCreateKey(w http.ResponseWriter, r *http.Request) {
	tid, ok := h.resolveTenant(w, r)
	if !ok {
		return
	}
	if h.keys == nil {
		http.NotFound(w, r)
		return
	}
	if !h.checkCSRF(w, r, tid) {
		return
	}
	label := strings.TrimSpace(r.FormValue("label"))
	if label == "" {
		label = "default"
	}
	tok, err := h.keys.IssueKey(r.Context(), tid.String(), label)
	if err != nil {
		h.serverError(w, "issue key", err)
		return
	}
	h.renderSetup(w, r, tid, normalizeWindow(r.URL.Query().Get("win")), tok)
}

// serveRevokeKey revokes a key and redirects back to Setup (post-redirect-get:
// nothing secret to show). A revoke of an unknown/already-revoked id logs and
// still redirects — the list simply reflects the current state.
func (h *Handler) serveRevokeKey(w http.ResponseWriter, r *http.Request) {
	tid, ok := h.resolveTenant(w, r)
	if !ok {
		return
	}
	if h.keys == nil {
		http.NotFound(w, r)
		return
	}
	if !h.checkCSRF(w, r, tid) {
		return
	}
	if err := h.keys.RevokeKey(r.Context(), tid.String(), r.PathValue("id")); err != nil {
		h.log.Error("web: revoke key", slog.Any("err", err))
	}
	http.Redirect(w, r, "/setup", http.StatusSeeOther)
}

// checkCSRF verifies the form CSRF token is present, valid, and bound to tenant.
// On failure it writes 403 and returns false. r.FormValue parses the POST body.
func (h *Handler) checkCSRF(w http.ResponseWriter, r *http.Request, tid tenant.ID) bool {
	if h.csrf == nil || !h.csrf.verify(tid.String(), r.FormValue("csrf_token")) {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return false
	}
	return true
}
