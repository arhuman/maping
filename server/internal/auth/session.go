// Package auth is the mAPI-ng dashboard authentication layer: OIDC login
// (GitHub/Google, no passwords — CONTEXT Member / roles), HMAC-signed session
// cookies, and a middleware that gates the dashboard to the caller's org.
//
// It is only wired in when a control plane is present (main.go). Three modes are
// selected at startup:
//
//   - No control plane: auth is OFF (constant dev tenant, Part 1 behavior).
//   - Control plane, no OIDC creds: dev-login only — a button that starts a
//     session as the seeded dev-org admin, for session-gated local testing.
//   - Control plane + OIDC creds: real GitHub/Google login; dev-login disabled.
//
// The session is a self-contained HMAC-signed cookie (no server-side store):
// base64(payload) + "." + base64(hmac-sha256(payload)). The org id in the
// verified session is what web.Config.Tenant reads, so a member only ever sees
// their own org's data. Secrets and access tokens are never logged.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// errBadSession is the single opaque error decode returns for any invalid
// cookie (malformed, wrong key, tampered, or expired). The caller treats it as
// "logged out" and never distinguishes the cause to a client, so tampering
// probes learn nothing.
var errBadSession = errors.New("auth: invalid session")

// Session is the identity carried in the signed cookie. Exp is an absolute
// expiry; a decoded session past Exp is rejected as logged out.
type Session struct {
	OrgID    string
	MemberID string
	Role     string
	Exp      time.Time
}

// signer signs and verifies session payloads with an HMAC-SHA256 key. The key
// must be >= 32 bytes (enforced by newSigner) so the MAC has full strength.
type signer struct {
	key []byte
}

// newSigner builds a signer, rejecting a key shorter than 32 bytes.
func newSigner(key []byte) (*signer, error) {
	if len(key) < 32 {
		return nil, fmt.Errorf("auth: session key must be >= 32 bytes, got %d", len(key))
	}
	return &signer{key: key}, nil
}

// mac computes the HMAC-SHA256 of payload under the signer's key.
func (s *signer) mac(payload []byte) []byte {
	m := hmac.New(sha256.New, s.key)
	m.Write(payload)
	return m.Sum(nil)
}

// encode serializes and signs a session into the cookie value
// base64url(payload) + "." + base64url(hmac). The payload is a fixed
// "orgID|memberID|role|expUnix" record; the fields carry no delimiter so the
// join is unambiguous (org/member ids are UUIDs, role is a constrained enum).
func (s *signer) encode(sess Session) string {
	payload := strings.Join([]string{
		sess.OrgID,
		sess.MemberID,
		sess.Role,
		strconv.FormatInt(sess.Exp.Unix(), 10),
	}, "|")
	p := []byte(payload)
	enc := base64.RawURLEncoding
	return enc.EncodeToString(p) + "." + enc.EncodeToString(s.mac(p))
}

// decode verifies and parses a cookie value into a Session. It returns
// errBadSession for any malformed input, MAC mismatch (constant-time compare),
// or expiry — never leaking which. now is injected so the expiry check is
// testable.
func (s *signer) decode(value string, now time.Time) (Session, error) {
	dot := strings.IndexByte(value, '.')
	if dot < 0 {
		return Session{}, errBadSession
	}
	enc := base64.RawURLEncoding
	payload, err := enc.DecodeString(value[:dot])
	if err != nil {
		return Session{}, errBadSession
	}
	gotMAC, err := enc.DecodeString(value[dot+1:])
	if err != nil {
		return Session{}, errBadSession
	}
	// Constant-time compare so a MAC-forging attacker learns nothing from timing.
	if !hmac.Equal(gotMAC, s.mac(payload)) {
		return Session{}, errBadSession
	}

	parts := strings.Split(string(payload), "|")
	if len(parts) != 4 {
		return Session{}, errBadSession
	}
	expUnix, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		return Session{}, errBadSession
	}
	exp := time.Unix(expUnix, 0)
	if !exp.After(now) {
		return Session{}, errBadSession
	}
	return Session{OrgID: parts[0], MemberID: parts[1], Role: parts[2], Exp: exp}, nil
}
