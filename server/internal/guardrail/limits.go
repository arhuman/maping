// Package guardrail holds the server-enforced, per-tenant limits that protect
// the ingest path from an untrusted client (CONTEXT Guardrails): ingest rate
// limit, cardinality cap, max payload size, and retention. It is the lowest
// layer in the ingest data flow: the control plane (which owns plans) depends
// on guardrail for the shared Limits type, and ingest depends on guardrail for
// enforcement — never the reverse. Retention itself is enforced as ClickHouse
// TTL (see storage migrations), not in code.
package guardrail

// Limits is the resolved per-tenant guardrail budget. It is the shared type the
// control plane fills from a plan_limits row and the ingest path enforces. It
// lives here (the lowest layer) so control can depend on guardrail without an
// import cycle (lang-go layer-dependency direction).
type Limits struct {
	// MaxRPS is the sustained ingest request rate per second per tenant.
	MaxRPS float64
	// Burst is the token-bucket burst allowance over MaxRPS.
	Burst int
	// CardinalityCap is the max number of distinct series tracked per tenant
	// per node before new series are frozen (best-effort, see Cardinality).
	CardinalityCap int
	// MaxPayloadBytes caps a single ingest request body.
	MaxPayloadBytes int64
	// RetentionDays is the raw-tier retention, mirrored by ClickHouse TTL.
	RetentionDays int
}

// DefaultLimits returns the fallback budget used when no control plane is wired
// (local dev / current tests). It mirrors the seeded free-plan row so behavior
// is identical whether or not Postgres is present.
func DefaultLimits() Limits {
	return Limits{
		MaxRPS:          100,
		Burst:           200,
		CardinalityCap:  10000,
		MaxPayloadBytes: 4 << 20, // 4 MiB
		RetentionDays:   30,
	}
}
