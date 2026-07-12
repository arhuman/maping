package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strings"
)

// csrf issues and verifies HMAC-signed per-session form tokens for the
// state-changing Setup POSTs (create/revoke key), mirroring the auth state-cookie
// pattern. A token binds a fresh random nonce to the caller's tenant under an
// HMAC key, so it is unforgeable without the key and only ever valid for the org
// it was minted for. It is a synchronizer token: the value is embedded in the
// authenticated /setup page's forms and echoed back on POST — a cross-site page
// cannot read it, so it cannot forge a valid submission. The session cookie is
// SameSite=Lax (blocking cross-site POST) and this is defense in depth.
type csrf struct {
	key []byte
}

// newCSRF builds a token signer, or returns nil when no key is configured (dev/
// no-control-plane, where the keys panel and its POSTs are absent anyway).
func newCSRF(key []byte) *csrf {
	if len(key) == 0 {
		return nil
	}
	return &csrf{key: key}
}

// mac computes the HMAC-SHA256 of payload under the csrf key.
func (c *csrf) mac(payload []byte) []byte {
	m := hmac.New(sha256.New, c.key)
	m.Write(payload)
	return m.Sum(nil)
}

// issue mints a signed token bound to tenant: base64url(tenant|nonce) + "." +
// base64url(hmac).
func (c *csrf) issue(tenant string) string {
	nonce := make([]byte, 16)
	// crypto/rand.Read never returns a short read; an RNG failure is unrecoverable.
	_, _ = rand.Read(nonce)
	payload := []byte(tenant + "|" + base64.RawURLEncoding.EncodeToString(nonce))
	enc := base64.RawURLEncoding
	return enc.EncodeToString(payload) + "." + enc.EncodeToString(c.mac(payload))
}

// verify reports whether token has a valid MAC and is bound to tenant. It uses a
// constant-time MAC compare so a forging probe learns nothing from timing.
func (c *csrf) verify(tenant, token string) bool {
	dot := strings.IndexByte(token, '.')
	if dot < 0 {
		return false
	}
	enc := base64.RawURLEncoding
	payload, err := enc.DecodeString(token[:dot])
	if err != nil {
		return false
	}
	gotMAC, err := enc.DecodeString(token[dot+1:])
	if err != nil {
		return false
	}
	if !hmac.Equal(gotMAC, c.mac(payload)) {
		return false
	}
	prefix := tenant + "|"
	return strings.HasPrefix(string(payload), prefix) && len(payload) > len(prefix)
}
