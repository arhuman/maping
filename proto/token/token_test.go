package token

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	cases := []struct{ origin, secret string }{
		{"https://ingest.example.com", "sekret123"},
		{"http://localhost:8080", "abc-DEF_09"},
		{"https://host:8443", "s"},
	}
	for _, c := range cases {
		tok := Encode(c.origin, c.secret)
		assert.True(t, strings.HasPrefix(tok, prefix), "token keeps the prefix")
		assert.Contains(t, tok, ".", "structured token has an origin separator")
		origin, secret, ok := Decode(tok)
		require.True(t, ok, "decode %q", tok)
		assert.Equal(t, c.origin, origin)
		assert.Equal(t, c.secret, secret)
	}
}

func TestEncodeNoOriginIsPlainPrefixed(t *testing.T) {
	tok := Encode("", "mysecret")
	assert.Equal(t, "mk_live_mysecret", tok)
	assert.NotContains(t, tok, ".", "no origin -> no separator")

	origin, secret, ok := Decode(tok)
	require.True(t, ok)
	assert.Empty(t, origin)
	assert.Equal(t, "mysecret", secret)
}

func TestDecodeLegacyBareKey(t *testing.T) {
	// The dev key and any pre-format key have no prefix: the whole value is the
	// secret and there is no origin. This is what keeps old keys resolving.
	origin, secret, ok := Decode("dev-key")
	require.True(t, ok)
	assert.Empty(t, origin)
	assert.Equal(t, "dev-key", secret)
}

func TestDecodeRejectsMalformed(t *testing.T) {
	// Empty token.
	_, _, ok := Decode("")
	assert.False(t, ok)

	// Structured shape but origin segment is not base64url.
	_, _, ok = Decode("mk_live_@@@notb64@@@.secret")
	assert.False(t, ok, "invalid origin base64 must fail")

	// Structured shape but empty secret.
	_, _, ok = Decode("mk_live_" + base64.RawURLEncoding.EncodeToString([]byte("http://x")) + ".")
	assert.False(t, ok, "empty secret must fail")
}

func TestDecodeIgnoresDotsInUnprefixedKeys(t *testing.T) {
	// A value without the prefix is never structured, even if it contains a dot;
	// the whole thing is the secret (base64url secrets never contain a dot, so a
	// real key can't hit this — it just guards the classifier).
	origin, secret, ok := Decode("weird.value")
	require.True(t, ok)
	assert.Empty(t, origin)
	assert.Equal(t, "weird.value", secret)
}

func TestNewSecretUnique(t *testing.T) {
	a, err := NewSecret()
	require.NoError(t, err)
	b, err := NewSecret()
	require.NoError(t, err)
	assert.NotEqual(t, a, b, "secrets must differ")
	assert.NotContains(t, a, ".", "secret must not contain the separator")
	assert.GreaterOrEqual(t, len(a), 40, "256-bit base64url secret is ~43 chars")

	// A generated secret round-trips inside a token.
	tok := Encode("https://c.example.com", a)
	origin, secret, ok := Decode(tok)
	require.True(t, ok)
	assert.Equal(t, "https://c.example.com", origin)
	assert.Equal(t, a, secret)
}
