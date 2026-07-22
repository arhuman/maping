package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/oauth2"
)

// fakeToken returns a minimal bearer token that satisfies tok.SetAuthHeader.
func fakeToken() *oauth2.Token {
	return &oauth2.Token{AccessToken: "test-access-token", TokenType: "bearer"}
}

// newJSONServer returns an httptest.Server that replies with statusCode and the
// JSON-marshalled body on every request.
func newJSONServer(t *testing.T, statusCode int, body any) *httptest.Server {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		_, _ = w.Write(b)
	}))
}

// TestGetJSON_HappyPath verifies that getJSON decodes a valid 200 response.
func TestGetJSON_HappyPath(t *testing.T) {
	srv := newJSONServer(t, http.StatusOK, map[string]any{"key": "value"})
	defer srv.Close()

	var got map[string]string
	err := getJSON(context.Background(), srv.Client(), srv.URL, fakeToken(), &got)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got["key"] != "value" {
		t.Errorf("got %q, want %q", got["key"], "value")
	}
}

// TestGetJSON_Non200 verifies that a non-200 response is returned as an error.
func TestGetJSON_Non200(t *testing.T) {
	srv := newJSONServer(t, http.StatusUnauthorized, map[string]any{})
	defer srv.Close()

	var got map[string]string
	err := getJSON(context.Background(), srv.Client(), srv.URL, fakeToken(), &got)
	if err == nil {
		t.Fatal("expected error for non-200 status, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error %q should mention status 401", err.Error())
	}
}

// TestGetJSON_MalformedBody verifies that a body that is not valid JSON is an error.
func TestGetJSON_MalformedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not json {{{"))
	}))
	defer srv.Close()

	var got map[string]string
	err := getJSON(context.Background(), srv.Client(), srv.URL, fakeToken(), &got)
	if err == nil {
		t.Fatal("expected error for malformed JSON body, got nil")
	}
}

// TestGetJSON_BodyTooLarge verifies that a body exceeding maxUserinfoBytes is rejected.
func TestGetJSON_BodyTooLarge(t *testing.T) {
	// Serve a body that is exactly maxUserinfoBytes+1 bytes of valid JSON-ish content.
	// We send it as a raw server so we control the exact byte count.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		// Write a JSON string that is too large: "{"a":"<maxUserinfoBytes bytes>"}
		// The easiest approach is a raw blob larger than the limit.
		w.Write([]byte(`"`))
		for i := 0; i <= maxUserinfoBytes; i++ {
			w.Write([]byte("x"))
		}
		w.Write([]byte(`"`))
	}))
	defer srv.Close()

	var got any
	err := getJSON(context.Background(), srv.Client(), srv.URL, fakeToken(), &got)
	if err == nil {
		t.Fatal("expected error for oversized body, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error %q should mention 'exceeds'", err.Error())
	}
}

// TestGetJSON_ClientError verifies that a transport-level error is returned as an error.
func TestGetJSON_ClientError(t *testing.T) {
	// Use a client pointing at a closed server to force a Do-level error.
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	srv.Close() // close immediately so connections are refused

	var got map[string]string
	err := getJSON(context.Background(), srv.Client(), srv.URL, fakeToken(), &got)
	if err == nil {
		t.Fatal("expected error for refused connection, got nil")
	}
}

// TestGetJSON_InvalidURL verifies that a malformed URL surfaces as a build-request error.
func TestGetJSON_InvalidURL(t *testing.T) {
	// A URL with a control character makes http.NewRequestWithContext fail.
	var got map[string]string
	err := getJSON(context.Background(), &http.Client{}, "://\x00bad", fakeToken(), &got)
	if err == nil {
		t.Fatal("expected error for invalid URL, got nil")
	}
}

// TestHttpUserinfo_UnknownProvider verifies the unknown-provider branch returns an error.
func TestHttpUserinfo_UnknownProvider(t *testing.T) {
	fn := httpUserinfo(&http.Client{})
	_, err := fn(context.Background(), "unknown", nil, fakeToken())
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
	if !strings.Contains(err.Error(), "unknown provider") {
		t.Errorf("error %q should mention 'unknown provider'", err.Error())
	}
}

// TestGithubIdentity_HappyPath verifies that githubIdentity extracts subject and
// email from the two GitHub userinfo endpoints correctly.
func TestGithubIdentity_HappyPath(t *testing.T) {
	// We need two distinct endpoints: /user and /user/emails.
	// Use an http.ServeMux to route them inside a single test server.
	mux := http.NewServeMux()
	mux.HandleFunc("/user", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": 99, "email": "public@example.com"})
	})
	mux.HandleFunc("/user/emails", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{
			{"email": "primary@example.com", "primary": true, "verified": true},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := srv.Client()
	// Point the production URLs to our test server by replacing the host prefix via a custom transport.
	client.Transport = rewriteTransport{base: srv.URL, inner: srv.Client().Transport}

	id, err := githubIdentity(context.Background(), client, nil, fakeToken())
	if err != nil {
		t.Fatalf("githubIdentity error: %v", err)
	}
	if id.Subject != "github:99" {
		t.Errorf("subject = %q, want github:99", id.Subject)
	}
	// The verified primary email should override the public profile email.
	if id.Email != "primary@example.com" {
		t.Errorf("email = %q, want primary@example.com", id.Email)
	}
}

// TestGithubIdentity_RejectsUnverifiedProfileEmail verifies that when /user/emails
// has no verified primary, the login is REJECTED rather than falling back to the
// (possibly unverified) public profile email. The identity is linked to an account
// by email downstream, so an unverified address must never reach a member row.
func TestGithubIdentity_RejectsUnverifiedProfileEmail(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/user", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": 42, "email": "profile@example.com"})
	})
	// /user/emails returns only unverified entries — no verified primary to trust.
	mux.HandleFunc("/user/emails", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{
			{"email": "secondary@example.com", "primary": false, "verified": false},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := srv.Client()
	client.Transport = rewriteTransport{base: srv.URL, inner: srv.Client().Transport}

	_, err := githubIdentity(context.Background(), client, nil, fakeToken())
	if err == nil {
		t.Fatal("expected error when no verified primary email is available")
	}
	if !strings.Contains(err.Error(), "no verified primary email") {
		t.Errorf("error %q should mention 'no verified primary email'", err.Error())
	}
}

// TestGithubIdentity_MissingID verifies that a zero GitHub user ID is rejected.
func TestGithubIdentity_MissingID(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/user", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// id omitted -> zero value
		json.NewEncoder(w).Encode(map[string]any{"email": "u@example.com"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := srv.Client()
	client.Transport = rewriteTransport{base: srv.URL, inner: srv.Client().Transport}

	_, err := githubIdentity(context.Background(), client, nil, fakeToken())
	if err == nil {
		t.Fatal("expected error for missing GitHub user id")
	}
	if !strings.Contains(err.Error(), "no id") {
		t.Errorf("error %q should mention 'no id'", err.Error())
	}
}

// TestGithubIdentity_EmailsEndpointFailure verifies that when the /user/emails
// lookup fails, the login is rejected: since the verified primary is now the ONLY
// trusted email source (no profile fallback), a failed lookup cannot yield an
// identity.
func TestGithubIdentity_EmailsEndpointFailure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/user", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": 7})
	})
	mux.HandleFunc("/user/emails", func(w http.ResponseWriter, _ *http.Request) {
		// Non-200 simulates the emails endpoint failing.
		http.Error(w, "not found", http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := srv.Client()
	client.Transport = rewriteTransport{base: srv.URL, inner: srv.Client().Transport}

	_, err := githubIdentity(context.Background(), client, nil, fakeToken())
	if err == nil {
		t.Fatal("expected error when the github emails lookup fails")
	}
	if !strings.Contains(err.Error(), "github emails") {
		t.Errorf("error %q should mention the failed 'github emails' lookup", err.Error())
	}
}

// TestGoogleIdentity_HappyPath verifies the normal Google subject+email extraction.
func TestGoogleIdentity_HappyPath(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth2/v3/userinfo", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"sub":            "1234567890",
			"email":          "user@gmail.com",
			"email_verified": true,
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := srv.Client()
	client.Transport = rewriteTransport{base: srv.URL, inner: srv.Client().Transport}

	id, err := googleIdentity(context.Background(), client, nil, fakeToken())
	if err != nil {
		t.Fatalf("googleIdentity error: %v", err)
	}
	if id.Subject != "google:1234567890" {
		t.Errorf("subject = %q, want google:1234567890", id.Subject)
	}
	if id.Email != "user@gmail.com" {
		t.Errorf("email = %q, want user@gmail.com", id.Email)
	}
}

// TestGoogleIdentity_MissingSub verifies that a missing sub field is rejected.
func TestGoogleIdentity_MissingSub(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth2/v3/userinfo", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"email":          "u@gmail.com",
			"email_verified": true,
			// sub omitted
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := srv.Client()
	client.Transport = rewriteTransport{base: srv.URL, inner: srv.Client().Transport}

	_, err := googleIdentity(context.Background(), client, nil, fakeToken())
	if err == nil {
		t.Fatal("expected error for missing Google sub")
	}
	if !strings.Contains(err.Error(), "no sub") {
		t.Errorf("error %q should mention 'no sub'", err.Error())
	}
}

// TestGoogleIdentity_UnverifiedEmail verifies that an unverified or missing email is rejected.
func TestGoogleIdentity_UnverifiedEmail(t *testing.T) {
	tests := []struct {
		name    string
		payload map[string]any
	}{
		{
			name: "email_verified false",
			payload: map[string]any{
				"sub":            "abc",
				"email":          "u@gmail.com",
				"email_verified": false,
			},
		},
		{
			name: "email missing",
			payload: map[string]any{
				"sub":            "abc",
				"email_verified": true,
				// email omitted
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := http.NewServeMux()
			payload := tt.payload
			mux.HandleFunc("/oauth2/v3/userinfo", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(payload)
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()

			client := srv.Client()
			client.Transport = rewriteTransport{base: srv.URL, inner: srv.Client().Transport}

			_, err := googleIdentity(context.Background(), client, nil, fakeToken())
			if err == nil {
				t.Fatalf("expected error for %s", tt.name)
			}
			if !strings.Contains(err.Error(), "no verified email") {
				t.Errorf("error %q should mention 'no verified email'", err.Error())
			}
		})
	}
}

// TestHttpUserinfo_GitHub verifies the httpUserinfo dispatcher routes to githubIdentity.
func TestHttpUserinfo_GitHub(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/user", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": 55, "email": "gh@example.com"})
	})
	mux.HandleFunc("/user/emails", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{
			{"email": "gh@example.com", "primary": true, "verified": true},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := srv.Client()
	client.Transport = rewriteTransport{base: srv.URL, inner: srv.Client().Transport}

	fn := httpUserinfo(client)
	id, err := fn(context.Background(), providerGitHub, nil, fakeToken())
	if err != nil {
		t.Fatalf("httpUserinfo github error: %v", err)
	}
	if id.Subject != "github:55" {
		t.Errorf("subject = %q, want github:55", id.Subject)
	}
}

// TestHttpUserinfo_Google verifies the httpUserinfo dispatcher routes to googleIdentity.
func TestHttpUserinfo_Google(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth2/v3/userinfo", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"sub":            "google-sub-123",
			"email":          "guser@gmail.com",
			"email_verified": true,
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := srv.Client()
	client.Transport = rewriteTransport{base: srv.URL, inner: srv.Client().Transport}

	fn := httpUserinfo(client)
	id, err := fn(context.Background(), providerGoogle, nil, fakeToken())
	if err != nil {
		t.Fatalf("httpUserinfo google error: %v", err)
	}
	if id.Subject != "google:google-sub-123" {
		t.Errorf("subject = %q, want google:google-sub-123", id.Subject)
	}
	if id.Email != "guser@gmail.com" {
		t.Errorf("email = %q, want guser@gmail.com", id.Email)
	}
}

// rewriteTransport rewrites outgoing requests so that a call to
// api.github.com/user or googleapis.com/... is redirected to base (the test
// server). It preserves the path so the test mux can route /user vs /user/emails.
type rewriteTransport struct {
	base  string // e.g. "http://127.0.0.1:PORT"
	inner http.RoundTripper
}

func (rt rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone the request and rewrite the host/scheme to the test server.
	clone := req.Clone(req.Context())
	clone.URL.Scheme = "http"
	// Parse the test server URL to extract host.
	testURL, _ := http.NewRequest(http.MethodGet, rt.base, nil)
	clone.URL.Host = testURL.URL.Host
	inner := rt.inner
	if inner == nil {
		inner = http.DefaultTransport
	}
	return inner.RoundTrip(clone)
}
