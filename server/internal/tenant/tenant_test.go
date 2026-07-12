package tenant

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRejectsEmpty(t *testing.T) {
	_, err := Parse("")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrEmpty)
}

func TestParseAcceptsNonEmpty(t *testing.T) {
	id, err := Parse("org-123")
	require.NoError(t, err)
	assert.Equal(t, "org-123", id.String())
	assert.False(t, id.IsZero())
}

func TestZeroValueIsInvalid(t *testing.T) {
	var id ID
	assert.True(t, id.IsZero())
	assert.Equal(t, "", id.String())
}

func TestMustParsePanicsOnEmpty(t *testing.T) {
	assert.PanicsWithError(t, ErrEmpty.Error(), func() { MustParse("") })
	assert.NotPanics(t, func() { MustParse("org-1") })
}

func TestIDIsComparableMapKey(t *testing.T) {
	// The isolation invariant relies on ID being usable as a map key, so existing
	// per-tenant maps keep working after the string -> ID migration.
	seen := map[ID]int{}
	seen[MustParse("a")]++
	seen[MustParse("a")]++
	seen[MustParse("b")]++
	assert.Equal(t, 2, seen[MustParse("a")])
	assert.Equal(t, 1, seen[MustParse("b")])
}

func TestErrEmptyIsSentinel(t *testing.T) {
	_, err := Parse("")
	assert.True(t, errors.Is(err, ErrEmpty))
}
