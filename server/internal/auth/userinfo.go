package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"golang.org/x/oauth2"
)

// maxUserinfoBytes caps the userinfo response body read (defense against a
// hostile or buggy provider streaming an unbounded body).
const maxUserinfoBytes = 1 << 20 // 1 MiB

// identity is the verified identity extracted from a provider's userinfo
// endpoint: a provider-prefixed stable subject (e.g. "github:12345") and a
// verified email. It is what the callback upserts into a member.
type identity struct {
	Subject string
	Email   string
}

// userinfoFunc fetches the verified identity for a completed OAuth exchange. It
// is a field on auth (like control's lookupFunc) so the callback is unit-
// testable with a fake identity and no real OAuth network call. tok carries the
// access token; the real implementation calls the provider over TLS.
type userinfoFunc func(ctx context.Context, name providerName, cfg *oauth2.Config, tok *oauth2.Token) (identity, error)

// httpUserinfo builds the production userinfoFunc using client for the userinfo
// HTTP calls. client MUST have an explicit timeout (main supplies one). The
// access token is sent as a bearer token over the provider's HTTPS endpoint and
// is never logged.
func httpUserinfo(client *http.Client) userinfoFunc {
	return func(ctx context.Context, name providerName, cfg *oauth2.Config, tok *oauth2.Token) (identity, error) {
		switch name {
		case providerGitHub:
			return githubIdentity(ctx, client, cfg, tok)
		case providerGoogle:
			return googleIdentity(ctx, client, cfg, tok)
		default:
			return identity{}, fmt.Errorf("auth: unknown provider %q", name)
		}
	}
}

// getJSON performs an authenticated GET and decodes the JSON body into v. It
// caps and drains the body per the http-client hygiene rules.
func getJSON(ctx context.Context, client *http.Client, url string, tok *oauth2.Token, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("auth: build request: %w", err)
	}
	tok.SetAuthHeader(req)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		if resp != nil {
			resp.Body.Close()
		}
		return fmt.Errorf("auth: userinfo request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxUserinfoBytes))
		return fmt.Errorf("auth: userinfo status %d", resp.StatusCode)
	}

	limited := &io.LimitedReader{R: resp.Body, N: maxUserinfoBytes + 1}
	body, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("auth: read userinfo: %w", err)
	}
	if limited.N == 0 {
		return fmt.Errorf("auth: userinfo body exceeds %d bytes", maxUserinfoBytes)
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("auth: decode userinfo: %w", err)
	}
	return nil
}

// githubIdentity reads the GitHub user id and a verified primary email.
func githubIdentity(ctx context.Context, client *http.Client, _ *oauth2.Config, tok *oauth2.Token) (identity, error) {
	var user struct {
		ID int64 `json:"id"`
	}
	if err := getJSON(ctx, client, "https://api.github.com/user", tok, &user); err != nil {
		return identity{}, err
	}
	if user.ID == 0 {
		return identity{}, fmt.Errorf("auth: github user has no id")
	}

	// Only ever trust a primary+verified address from the emails endpoint: the
	// public profile email (user.email) may be unverified or null, and the identity
	// is linked to an account by email downstream, so an unverified value must never
	// reach a member row.
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := getJSON(ctx, client, "https://api.github.com/user/emails", tok, &emails); err != nil {
		return identity{}, fmt.Errorf("auth: github emails: %w", err)
	}
	email := ""
	for _, e := range emails {
		if e.Primary && e.Verified {
			email = e.Email
			break
		}
	}
	if email == "" {
		return identity{}, fmt.Errorf("auth: github: no verified primary email")
	}
	return identity{Subject: fmt.Sprintf("github:%d", user.ID), Email: email}, nil
}

// googleIdentity reads the Google subject and verified email from the OpenID
// userinfo endpoint.
func googleIdentity(ctx context.Context, client *http.Client, _ *oauth2.Config, tok *oauth2.Token) (identity, error) {
	var info struct {
		Sub           string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
	}
	if err := getJSON(ctx, client, "https://www.googleapis.com/oauth2/v3/userinfo", tok, &info); err != nil {
		return identity{}, err
	}
	if info.Sub == "" {
		return identity{}, fmt.Errorf("auth: google userinfo has no sub")
	}
	if !info.EmailVerified || info.Email == "" {
		return identity{}, fmt.Errorf("auth: google: no verified email")
	}
	return identity{Subject: "google:" + info.Sub, Email: info.Email}, nil
}
