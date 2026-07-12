package web

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeKeyAdmin is a scriptable KeyAdmin for the Setup handler tests: it records
// the issue label and revoked id, and returns the scripted key list/token.
type fakeKeyAdmin struct {
	keys       []KeyInfo
	issued     string // token IssueKey returns
	issueLabel string // captured label of the last IssueKey call
	revokedID  string // captured id of the last RevokeKey call
}

func (f *fakeKeyAdmin) IssueKey(_ context.Context, _, label string) (string, error) {
	f.issueLabel = label
	return f.issued, nil
}
func (f *fakeKeyAdmin) ListKeys(context.Context, string) ([]KeyInfo, error) { return f.keys, nil }
func (f *fakeKeyAdmin) RevokeKey(_ context.Context, _, id string) error {
	f.revokedID = id
	return nil
}

// testCSRFKey is a 32-byte HMAC key for the Setup form tokens in tests.
var testCSRFKey = []byte("0123456789abcdef0123456789abcdef")

// csrfToken pulls the current form CSRF token out of the rendered Setup page.
var csrfRe = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)

func csrfToken(t *testing.T, base string) string {
	t.Helper()
	_, body := getBody(t, base+"/setup")
	m := csrfRe.FindStringSubmatch(body)
	require.Len(t, m, 2, "setup form must carry a csrf token")
	return m[1]
}

func TestSetupListsKeys(t *testing.T) {
	admin := &fakeKeyAdmin{keys: []KeyInfo{
		{ID: "k1", Label: "checkout-api", Last4: "a91f", CreatedAt: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)},
	}}
	srv := newServer(t, Config{Querier: fakeQuerier{}, Tenant: constTenant, KeyAdmin: admin, CSRFKey: testCSRFKey})

	code, body := getBody(t, srv.URL+"/setup")
	assert.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, "checkout-api")
	assert.Contains(t, body, "····a91f") // masked last-4
	assert.Contains(t, body, "2026-07-01")
	assert.Contains(t, body, `action="/setup/keys"`) // create form present
	assert.Contains(t, body, "HANDSHAKE")            // stepper still rendered
}

func TestSetupCreateRevealsOnce(t *testing.T) {
	admin := &fakeKeyAdmin{issued: "mk_live_abc.def456"}
	srv := newServer(t, Config{Querier: fakeQuerier{}, Tenant: constTenant, KeyAdmin: admin, CSRFKey: testCSRFKey})

	resp, err := http.PostForm(srv.URL+"/setup/keys", url.Values{
		"csrf_token": {csrfToken(t, srv.URL)},
		"label":      {"payments"},
	})
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "payments", admin.issueLabel)
	body := readBody(t, resp)
	assert.Contains(t, body, "mk_live_abc.def456") // token revealed once
	assert.Contains(t, body, "COPY IT NOW")
	assert.Contains(t, body, `data-copy="mp-newkey"`) // copy hook (slice 6)
	assert.Contains(t, body, `src="/assets/copy.js"`) // helper loaded
}

func TestSetupCreateDefaultsLabel(t *testing.T) {
	admin := &fakeKeyAdmin{issued: "mk_live_x.y"}
	srv := newServer(t, Config{Querier: fakeQuerier{}, Tenant: constTenant, KeyAdmin: admin, CSRFKey: testCSRFKey})

	resp, err := http.PostForm(srv.URL+"/setup/keys", url.Values{
		"csrf_token": {csrfToken(t, srv.URL)},
	})
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, "default", admin.issueLabel, "empty label falls back to default")
}

func TestSetupRevokeRemoves(t *testing.T) {
	admin := &fakeKeyAdmin{keys: []KeyInfo{{ID: "k9", Label: "old", Last4: "dead"}}}
	srv := newServer(t, Config{Querier: fakeQuerier{}, Tenant: constTenant, KeyAdmin: admin, CSRFKey: testCSRFKey})

	// Don't follow the redirect so we can assert the 303.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	form := url.Values{"csrf_token": {csrfToken(t, srv.URL)}}
	resp, err := client.PostForm(srv.URL+"/setup/keys/k9/revoke", form)
	require.NoError(t, err)
	resp.Body.Close()

	assert.Equal(t, http.StatusSeeOther, resp.StatusCode)
	assert.Equal(t, "/setup", resp.Header.Get("Location"))
	assert.Equal(t, "k9", admin.revokedID)
}

func TestSetupRevokedKeyDropsAction(t *testing.T) {
	revoked := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)
	admin := &fakeKeyAdmin{keys: []KeyInfo{{ID: "k1", Label: "gone", Last4: "beef", RevokedAt: &revoked}}}
	srv := newServer(t, Config{Querier: fakeQuerier{}, Tenant: constTenant, KeyAdmin: admin, CSRFKey: testCSRFKey})

	_, body := getBody(t, srv.URL+"/setup")
	assert.Contains(t, body, "revoked")
	assert.NotContains(t, body, `action="/setup/keys/k1/revoke"`, "revoked key has no revoke form")
}

func TestSetupCSRFRejectsForgedPost(t *testing.T) {
	admin := &fakeKeyAdmin{}
	srv := newServer(t, Config{Querier: fakeQuerier{}, Tenant: constTenant, KeyAdmin: admin, CSRFKey: testCSRFKey})

	// A forged token must be rejected, and no key issued.
	resp, err := http.PostForm(srv.URL+"/setup/keys", url.Values{
		"csrf_token": {"forged.token"},
		"label":      {"evil"},
	})
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Empty(t, admin.issueLabel, "forged CSRF must not reach IssueKey")

	// A missing token is equally rejected.
	resp2, err := http.PostForm(srv.URL+"/setup/keys", url.Values{"label": {"evil"}})
	require.NoError(t, err)
	resp2.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp2.StatusCode)
}

func TestSetupNilKeyAdminHidesPanel(t *testing.T) {
	srv := newServer(t, Config{Querier: fakeQuerier{}, Tenant: constTenant})

	code, body := getBody(t, srv.URL+"/setup")
	assert.Equal(t, http.StatusOK, code)
	assert.NotContains(t, body, `action="/setup/keys"`, "no control plane -> no create form")
	assert.Contains(t, body, "dev-key") // the static-key explanation
	assert.Contains(t, body, "HANDSHAKE")

	// The key POST routes 404 without a KeyAdmin.
	resp, err := http.PostForm(srv.URL+"/setup/keys", url.Values{})
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// readBody drains and returns a response body as a string.
func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(b)
}
