// Package tenant defines the TenantID value type: the organisation identity that
// scopes every data-plane read and write. It exists so a tenant cannot travel
// through the system as an unvalidated bare string. The identity is produced by
// a boundary (the ingest key resolver, or the dashboard session) and parsed once
// into an ID; from there the storage layer accepts only an ID, so an empty or
// unvalidated tenant reaching a query or a row is unrepresentable rather than
// merely discouraged (ADR-0010 extends the read-boundary guarantee to a type).
package tenant

import "errors"

// ErrEmpty is returned by Parse when the identifier is empty.
var ErrEmpty = errors.New("tenant: empty id")

// ID is a validated organisation identifier. The zero value is invalid; the only
// way to obtain a non-zero ID is Parse (or MustParse for trusted constants and
// tests), so an ID in hand is always non-empty. It is a comparable value type,
// so it still works as a map key for the existing per-tenant maps.
type ID struct {
	v string
}

// Parse validates s and returns the corresponding ID. An empty string yields
// ErrEmpty; any non-empty organisation id is accepted (the control plane is the
// source of truth for which ids exist — Parse enforces only non-emptiness, the
// invariant the storage layer relies on).
func Parse(s string) (ID, error) {
	if s == "" {
		return ID{}, ErrEmpty
	}
	return ID{v: s}, nil
}

// MustParse is Parse that panics on error. Use only for compile-time-known
// constants (e.g. the dev tenant) and tests, never for request input.
func MustParse(s string) ID {
	id, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return id
}

// String returns the underlying identifier for logging and SQL binding.
func (id ID) String() string { return id.v }

// IsZero reports whether id is the zero (invalid) value.
func (id ID) IsZero() bool { return id.v == "" }
