package app

import (
	"github.com/arhuman/maping/server/internal/guardrail"
)

// LimitProvider, Limits, and DefaultLimits re-export the guardrail limit types as
// a PUBLIC surface. A composing build lives in its own module and cannot import
// server/internal/guardrail (the internal rule), so without these aliases it
// could not name the types WithLimitProvider requires and thus could not
// implement the seam. They are type aliases, so a provider written against
// app.LimitProvider IS a guardrail.LimitProvider — the ingest guardrails resolve
// through it with no adapter.
type (
	// LimitProvider is the per-tenant limits source the ingest guardrails resolve
	// through. A composing build implements it to layer its own behavior (e.g. an
	// account lifecycle) over the core plan budget.
	LimitProvider = guardrail.LimitProvider
	// Limits is a tenant's resolved guardrail budget (rate, burst, cardinality,
	// payload, retention).
	Limits = guardrail.Limits
)

// DefaultLimits is the free-tier budget a provider falls back to for an unknown
// plan. Re-exported so a composing provider can return it without importing the
// internal guardrail package.
func DefaultLimits() Limits { return guardrail.DefaultLimits() }
