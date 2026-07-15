package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeMemberAdmin is a scriptable MemberAdmin for the team-panel tests.
type fakeMemberAdmin struct {
	members     []MemberInfo
	invites     []InviteInfo
	used, limit int
	link        string // CreateInvite returns this accept link
	createErr   error
	createEmail string // captured email of the last CreateInvite
	createRole  string
	revokedID   string // captured id of the last RevokeInvite
	removedID   string // captured id of the last RemoveMember
}

func (f *fakeMemberAdmin) ListMembers(context.Context, string) ([]MemberInfo, error) {
	return f.members, nil
}
func (f *fakeMemberAdmin) ListInvites(context.Context, string) ([]InviteInfo, error) {
	return f.invites, nil
}
func (f *fakeMemberAdmin) CreateInvite(_ context.Context, _, _, email, role string) (string, error) {
	f.createEmail, f.createRole = email, role
	if f.createErr != nil {
		return "", f.createErr
	}
	return f.link, nil
}
func (f *fakeMemberAdmin) RevokeInvite(_ context.Context, _, id string) error {
	f.revokedID = id
	return nil
}
func (f *fakeMemberAdmin) RemoveMember(_ context.Context, _, id string) error {
	f.removedID = id
	return nil
}
func (f *fakeMemberAdmin) SeatUsage(context.Context, string) (int, int, error) {
	return f.used, f.limit, nil
}

// adminRole / memberRole are Role resolvers for the team-panel tests.
func adminRole(*http.Request) (string, string, bool)  { return "admin", "m-admin", true }
func memberRole(*http.Request) (string, string, bool) { return "member", "m-2", true }

func teamConfig(admin *fakeMemberAdmin, role RoleResolver) Config {
	return Config{
		Querier:     fakeQuerier{},
		Tenant:      constTenant,
		MemberAdmin: admin,
		Role:        role,
		// A KeyAdmin is always present alongside MemberAdmin in a real deployment
		// (both need the control plane); its form gives every member a valid,
		// tenant-bound CSRF token to scrape, so the admin-gate test exercises the
		// gate rather than failing at CSRF for lack of a token.
		KeyAdmin: &fakeKeyAdmin{},
		CSRFKey:  testCSRFKey,
	}
}

func TestTeamPanelListsMembersAndInvitesForAdmin(t *testing.T) {
	admin := &fakeMemberAdmin{
		used: 2, limit: 5,
		members: []MemberInfo{
			{ID: "m1", Email: "founder@x.io", Role: "admin", IsOwner: true, CreatedAt: time.Now()},
			{ID: "m2", Email: "dev@x.io", Role: "member", CreatedAt: time.Now()},
		},
		invites: []InviteInfo{
			{ID: "i1", Email: "new@x.io", Role: "member", ExpiresAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)},
		},
	}
	srv := newServer(t, teamConfig(admin, adminRole))

	code, body := getBody(t, srv.URL+"/setup")
	assert.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, "founder@x.io")
	assert.Contains(t, body, "· owner")
	assert.Contains(t, body, "dev@x.io")
	assert.Contains(t, body, "new@x.io") // pending invite
	assert.Contains(t, body, "2 of 5 seats")
	assert.Contains(t, body, `action="/setup/invites"`)             // admin sees invite form
	assert.Contains(t, body, `action="/setup/members/m2/remove"`)   // remove for a member
	assert.NotContains(t, body, `action="/setup/members/m1/remove"`) // never for the owner
}

func TestTeamPanelHidesActionsForMember(t *testing.T) {
	admin := &fakeMemberAdmin{used: 1, limit: 5, members: []MemberInfo{
		{ID: "m2", Email: "dev@x.io", Role: "member"},
	}}
	srv := newServer(t, teamConfig(admin, memberRole))

	_, body := getBody(t, srv.URL+"/setup")
	assert.Contains(t, body, "dev@x.io")                 // panel still visible
	assert.NotContains(t, body, `action="/setup/invites"`) // no invite form for a non-admin
	assert.NotContains(t, body, "/remove")                 // no remove buttons
	assert.Contains(t, body, "Only admins can invite")
}

func TestTeamCreateInviteRevealsLinkOnce(t *testing.T) {
	admin := &fakeMemberAdmin{used: 1, limit: 5, link: "https://maping.example.com/invite/secret123"}
	srv := newServer(t, teamConfig(admin, adminRole))

	resp, err := http.PostForm(srv.URL+"/setup/invites", url.Values{
		"csrf_token": {csrfToken(t, srv.URL)},
		"email":      {"teammate@x.io"},
		"role":       {"member"},
	})
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "teammate@x.io", admin.createEmail)
	assert.Equal(t, "member", admin.createRole)
	body := readBody(t, resp)
	assert.Contains(t, body, "https://maping.example.com/invite/secret123")
	assert.Contains(t, body, `data-copy="mp-invite"`)
}

func TestTeamCreateInviteSeatLimitShowsError(t *testing.T) {
	admin := &fakeMemberAdmin{used: 5, limit: 5, createErr: errors.New("seat limit")}
	srv := newServer(t, teamConfig(admin, adminRole))

	resp, err := http.PostForm(srv.URL+"/setup/invites", url.Values{
		"csrf_token": {csrfToken(t, srv.URL)},
		"email":      {"teammate@x.io"},
		"role":       {"member"},
	})
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode) // rendered, not 500
	assert.Contains(t, readBody(t, resp), "seat limit")
}

func TestTeamInviteRequiresAdmin(t *testing.T) {
	admin := &fakeMemberAdmin{used: 1, limit: 5}
	srv := newServer(t, teamConfig(admin, memberRole)) // caller is a member

	resp, err := http.PostForm(srv.URL+"/setup/invites", url.Values{
		"csrf_token": {csrfToken(t, srv.URL)},
		"email":      {"x@y.io"},
		"role":       {"member"},
	})
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Empty(t, admin.createEmail, "a non-admin must not reach CreateInvite")
}

func TestTeamRevokeInviteRedirects(t *testing.T) {
	admin := &fakeMemberAdmin{used: 1, limit: 5}
	srv := newServer(t, teamConfig(admin, adminRole))

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.PostForm(srv.URL+"/setup/invites/i7/revoke",
		url.Values{"csrf_token": {csrfToken(t, srv.URL)}})
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusSeeOther, resp.StatusCode)
	assert.Equal(t, "i7", admin.revokedID)
}

func TestTeamRemoveMemberRedirects(t *testing.T) {
	admin := &fakeMemberAdmin{used: 2, limit: 5}
	srv := newServer(t, teamConfig(admin, adminRole))

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.PostForm(srv.URL+"/setup/members/m9/remove",
		url.Values{"csrf_token": {csrfToken(t, srv.URL)}})
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusSeeOther, resp.StatusCode)
	assert.Equal(t, "m9", admin.removedID)
}

func TestTeamNilMemberAdminHidesPanelAnd404s(t *testing.T) {
	srv := newServer(t, Config{Querier: fakeQuerier{}, Tenant: constTenant})

	_, body := getBody(t, srv.URL+"/setup")
	assert.NotContains(t, body, `action="/setup/invites"`)

	resp, err := http.PostForm(srv.URL+"/setup/invites", url.Values{})
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}
