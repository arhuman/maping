package storage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/arhuman/maping/server/internal/tenant"
)

// TestEnqueueRejectsEmptyTenant is the write-path half of ADR-0010: a row that
// carries the zero-value tenant.ID must fail closed rather than be persisted
// under an empty tenant. The rejected row never reaches the buffer, so the
// batcher (running with no live connection here) never touches the conn.
func TestEnqueueRejectsEmptyTenant(t *testing.T) {
	w := newWriterWithConn(nil, Config{}, nil)
	t.Cleanup(func() { _ = w.Close(context.Background()) })

	require.ErrorIs(t, w.Enqueue(Row{Service: "svc"}), ErrEmptyTenant)
	require.ErrorIs(t,
		w.EnqueueInstanceWindow(InstanceWindowRow{Service: "svc"}),
		ErrEmptyTenant,
	)

	// Sanity: a resolved tenant passes the guard. (Assert the guard alone, not a
	// live insert — persistence is covered by the integration suite.)
	if tenant.MustParse("acme").IsZero() {
		t.Fatal("MustParse returned a zero tenant.ID")
	}
}
