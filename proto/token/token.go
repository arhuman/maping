// Package token is the mAPI-ng ingest-key wire format, shared by the client SDK
// (which derives its collector endpoint from the key) and the server (which
// authenticates the key). Keeping it in the proto module — the one module both
// sides already import — means client and server agree on the grammar by
// construction.
//
// A key is:
//
//	mk_live_<base64url(origin)>.<secret>
//
// where origin is the full collector origin (scheme://host[:port]) and secret is
// the random credential. The origin is client-only routing metadata: the server
// hashes and stores ONLY the secret (see Decode), so the embedded origin never
// affects auth and can change without invalidating a key. A key with no origin
// segment (mk_live_<secret>) and a bare legacy key with no prefix (e.g. the dev
// "dev-key") both decode to that whole value as the secret, so old keys keep
// working.
package token

import (
	"crypto/rand"
	"encoding/base64"
	"strings"
)

// prefix marks a mAPI-ng-issued key. Its presence (with a '.') is what
// distinguishes a structured token from a bare legacy secret.
const prefix = "mk_live_"

// secretBytes is the entropy of a generated secret (256 bits).
const secretBytes = 32

// Encode builds the key for a secret, embedding origin when non-empty. base64url
// (no padding) keeps the origin free of '.', so the '.' unambiguously separates
// the origin segment from the secret.
func Encode(origin, secret string) string {
	if origin == "" {
		return prefix + secret
	}
	return prefix + base64.RawURLEncoding.EncodeToString([]byte(origin)) + "." + secret
}

// Decode parses a key into its origin (empty if none) and secret. It accepts
// three forms: a structured token (mk_live_<b64(origin)>.<secret>), a prefixed
// keyless-origin token (mk_live_<secret>), and a bare legacy secret (no prefix).
// ok is false only for an empty token or a structured token whose origin segment
// is not valid base64url or whose secret is empty.
func Decode(tok string) (origin, secret string, ok bool) {
	if tok == "" {
		return "", "", false
	}
	body := tok
	if strings.HasPrefix(tok, prefix) {
		body = tok[len(prefix):]
		// Only a prefixed token can be structured; a '.' then splits origin.secret.
		if i := strings.IndexByte(body, '.'); i >= 0 {
			return decodeStructured(body[:i], body[i+1:])
		}
	}
	// Plain form: the whole body is the secret (prefixed keyless-origin or legacy).
	if body == "" {
		return "", "", false
	}
	return "", body, true
}

// decodeStructured parses the two segments of a structured token: originB64 must
// be valid base64url and sec must be non-empty.
func decodeStructured(originB64, sec string) (origin, secret string, ok bool) {
	raw, err := base64.RawURLEncoding.DecodeString(originB64)
	if err != nil || sec == "" {
		return "", "", false
	}
	return string(raw), sec, true
}

// NewSecret returns a fresh random secret as unpadded base64url (no '.'), safe to
// embed as the second segment of a key.
func NewSecret() (string, error) {
	b := make([]byte, secretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
