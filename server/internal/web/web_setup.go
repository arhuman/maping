package web

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/arhuman/maping/server/internal/tenant"
)

// setupExtras carries the reveal-once and error strings a Setup re-render may
// need after a POST: the ingest-key plaintext, the invite accept link, and a team
// error (e.g. seat limit reached). All empty on a plain GET, so nothing lingers.
type setupExtras struct {
	newToken  string
	inviteURL string
	teamError string
}

// serveSetup renders the always-reachable Setup page: the live handshake stepper
// and — when a control plane is present — the self-serve ingest-keys and team
// panels. Management is here (not gated on "no data yet") so a running tenant can
// still mint keys and invite teammates.
func (h *Handler) serveSetup(w http.ResponseWriter, r *http.Request) {
	tid, ok := h.resolveTenant(w, r)
	if !ok {
		return
	}
	winKey := normalizeWindow(r.URL.Query().Get("win"))
	h.renderSetup(w, r, tid, winKey, setupExtras{})
}

// caller returns the request's role and member id via the injected resolver, or
// ok=false when there is no control plane / no session.
func (h *Handler) caller(r *http.Request) (role, memberID string, ok bool) {
	if h.roleOf == nil {
		return "", "", false
	}
	return h.roleOf(r)
}

// isAdmin reports whether the caller is an org admin, the gate on team writes.
func (h *Handler) isAdmin(r *http.Request) bool {
	role, _, ok := h.caller(r)
	return ok && role == "admin"
}

// renderSetup builds the Setup page from the live handshake state plus the keys
// and team panels. ex carries the reveal-once plaintext/link right after a create
// POST; it is empty on a plain GET so a banner shows only once, by construction.
func (h *Handler) renderSetup(w http.ResponseWriter, r *http.Request, tid tenant.ID, winKey string, ex setupExtras) {
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
		NewToken:  ex.newToken,
		Refresh:   refresh,
	}
	if h.keys != nil {
		h.populateKeys(r, tid, &page)
	}
	if h.members != nil {
		h.populateTeam(r, tid, ex, &page)
	}
	h.render(w, "setup", page)
}

// populateKeys fills the keys-panel fields (CSRF token + key list) on page.
func (h *Handler) populateKeys(r *http.Request, tid tenant.ID, page *setupPage) {
	page.ShowKeys = true
	page.CSRFToken = h.csrf.issue(tid.String())
	infos, err := h.keys.ListKeys(r.Context(), tid.String())
	if err != nil {
		h.log.Error("web: list keys", slog.Any("err", err))
		return
	}
	page.Keys = toKeyRows(infos)
}

// populateTeam fills the team-panel fields (seat usage, members, invites, and the
// reveal-once/error strings) on page, admin-gating handled in the template.
func (h *Handler) populateTeam(r *http.Request, tid tenant.ID, ex setupExtras, page *setupPage) {
	page.ShowTeam = true
	page.IsAdmin = h.isAdmin(r)
	page.InviteURL = ex.inviteURL
	page.TeamError = ex.teamError
	if page.CSRFToken == "" {
		page.CSRFToken = h.csrf.issue(tid.String())
	}
	if used, limit, err := h.members.SeatUsage(r.Context(), tid.String()); err != nil {
		h.log.Error("web: seat usage", slog.Any("err", err))
	} else {
		page.SeatUsed, page.SeatLimit = used, limit
	}
	if ms, err := h.members.ListMembers(r.Context(), tid.String()); err != nil {
		h.log.Error("web: list members", slog.Any("err", err))
	} else {
		page.Members = mapSlice(ms, func(m MemberInfo) memberRow {
			return memberRow{ID: m.ID, Email: m.Email, Role: m.Role, IsOwner: m.IsOwner, Created: m.CreatedAt.Format("2006-01-02")}
		})
	}
	if is, err := h.members.ListInvites(r.Context(), tid.String()); err != nil {
		h.log.Error("web: list invites", slog.Any("err", err))
	} else {
		page.Invites = mapSlice(is, func(i InviteInfo) inviteRow {
			return inviteRow{ID: i.ID, Email: i.Email, Role: i.Role, Expires: i.ExpiresAt.Format("2006-01-02")}
		})
	}
}

// mapSlice maps a slice through f. It is the shared shape of the row adapters, so
// each stays a single expression rather than a near-identical range loop.
func mapSlice[A, B any](in []A, f func(A) B) []B {
	out := make([]B, 0, len(in))
	for _, v := range in {
		out = append(out, f(v))
	}
	return out
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
	h.renderSetup(w, r, tid, normalizeWindow(r.URL.Query().Get("win")), setupExtras{newToken: tok})
}

// serveCreateInvite mints a member invite and re-renders Setup with the reveal-once
// accept link. Admin-only; a seat-limit rejection is shown as a team error rather
// than a 500 (an expected outcome the admin should see).
func (h *Handler) serveCreateInvite(w http.ResponseWriter, r *http.Request) {
	tid, ok := h.teamAction(w, r)
	if !ok {
		return
	}
	email := strings.TrimSpace(r.FormValue("email"))
	role := r.FormValue("role")
	if email == "" || (role != "admin" && role != "member") {
		http.Error(w, "invalid invite (email and role required)", http.StatusBadRequest)
		return
	}
	_, memberID, _ := h.caller(r)
	win := normalizeWindow(r.URL.Query().Get("win"))
	link, err := h.members.CreateInvite(r.Context(), tid.String(), memberID, email, role)
	if err != nil {
		// Seat limit (and any other business rejection) is an expected outcome: show
		// it in the panel rather than 500-ing.
		h.log.Error("web: create invite", slog.Any("err", err))
		h.renderSetup(w, r, tid, win, setupExtras{teamError: "Could not create the invite — the team may be at its seat limit for this plan."})
		return
	}
	h.renderSetup(w, r, tid, win, setupExtras{inviteURL: link})
}

// serveRevokeInvite deletes a pending invite and redirects back to Setup (PRG).
// Admin-only. A revoke of an unknown/accepted id logs and still redirects.
//
//nolint:dupl // thin adapter over teamMutation; only the store call/label differ.
func (h *Handler) serveRevokeInvite(w http.ResponseWriter, r *http.Request) {
	h.teamMutation(w, r, "revoke invite", func(ctx context.Context, orgID, id string) error {
		return h.members.RevokeInvite(ctx, orgID, id)
	})
}

// serveRemoveMember removes a member and redirects back to Setup (PRG). Admin-only.
// The store refuses to remove the billing owner or the last admin; such a rejection
// logs and still redirects, leaving the member listed.
//
//nolint:dupl // thin adapter over teamMutation; only the store call/label differ.
func (h *Handler) serveRemoveMember(w http.ResponseWriter, r *http.Request) {
	h.teamMutation(w, r, "remove member", func(ctx context.Context, orgID, id string) error {
		return h.members.RemoveMember(ctx, orgID, id)
	})
}

// teamAction runs the shared preamble for every team POST: resolve the tenant,
// require a control plane, verify CSRF, and require an admin. It writes the
// appropriate error and returns ok=false on any failure.
func (h *Handler) teamAction(w http.ResponseWriter, r *http.Request) (tenant.ID, bool) {
	tid, ok := h.resolveTenant(w, r)
	if !ok {
		return tenant.ID{}, false
	}
	if h.members == nil {
		http.NotFound(w, r)
		return tenant.ID{}, false
	}
	if !h.checkCSRF(w, r, tid) {
		return tenant.ID{}, false
	}
	if !h.isAdmin(r) {
		http.Error(w, "admin only", http.StatusForbidden)
		return tenant.ID{}, false
	}
	return tid, true
}

// teamMutation is the shared shape of the id-then-redirect team writes (revoke
// invite, remove member): gate via teamAction, run do with the path id (a business
// rejection logs and still redirects, PRG), then redirect back to Setup.
func (h *Handler) teamMutation(w http.ResponseWriter, r *http.Request, label string, do func(ctx context.Context, orgID, id string) error) {
	tid, ok := h.teamAction(w, r)
	if !ok {
		return
	}
	if err := do(r.Context(), tid.String(), r.PathValue("id")); err != nil {
		h.log.Error("web: "+label, slog.Any("err", err))
	}
	http.Redirect(w, r, "/setup", http.StatusSeeOther)
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
