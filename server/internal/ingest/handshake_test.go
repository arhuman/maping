package ingest

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mapingv1 "github.com/arhuman/maping/proto/maping/v1"
)

// fakeRecorder captures RecordHandshake calls and can be forced to fail, so the
// tests can assert both the happy path (persisted) and the log-and-continue
// contract (a write error must not fail the handshake).
type fakeRecorder struct {
	mu      sync.Mutex
	calls   []recordCall
	failNow bool
}

type recordCall struct {
	tenant, service, instance, sdk string
}

func (r *fakeRecorder) RecordHandshake(_ context.Context, tenant, service, instance, sdk string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failNow {
		return errors.New("control write failed")
	}
	r.calls = append(r.calls, recordCall{tenant, service, instance, sdk})
	return nil
}

func (r *fakeRecorder) snapshot() []recordCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordCall(nil), r.calls...)
}

func TestRegisterRecordsHandshake(t *testing.T) {
	rec := &fakeRecorder{}
	resolver := NewStaticKeyResolver(map[string]string{"dev-key": "dev-tenant"})
	h := NewHandler(resolver, &fakeSink{}, nil, WithHandshakeRecorder(rec))

	msg := &mapingv1.Handshake{Service: "checkout", Instance: "pod-a", SdkVersion: "v1.0.0"}
	resp, err := h.Register(context.Background(), withBearer(msg, "dev-key"))
	require.NoError(t, err)
	assert.True(t, resp.Msg.GetAccepted())

	calls := rec.snapshot()
	require.Len(t, calls, 1)
	assert.Equal(t, recordCall{"dev-tenant", "checkout", "pod-a", "v1.0.0"}, calls[0])
}

func TestRegisterRecorderErrorDoesNotFailHandshake(t *testing.T) {
	rec := &fakeRecorder{failNow: true}
	resolver := NewStaticKeyResolver(map[string]string{"dev-key": "dev-tenant"})
	h := NewHandler(resolver, &fakeSink{}, nil, WithHandshakeRecorder(rec))

	resp, err := h.Register(context.Background(), withBearer(&mapingv1.Handshake{Service: "s"}, "dev-key"))
	require.NoError(t, err, "a recorder write error must not fail the handshake")
	assert.True(t, resp.Msg.GetAccepted())
}

func TestRegisterUnknownKeyDoesNotRecord(t *testing.T) {
	rec := &fakeRecorder{}
	resolver := NewStaticKeyResolver(map[string]string{"dev-key": "dev-tenant"})
	h := NewHandler(resolver, &fakeSink{}, nil, WithHandshakeRecorder(rec))

	_, err := h.Register(context.Background(), withBearer(&mapingv1.Handshake{Service: "s"}, "bad-key"))
	require.Error(t, err)
	assert.Empty(t, rec.snapshot(), "auth failure must never record a handshake")
}

func TestRegisterNilRecorderIsDefault(t *testing.T) {
	// No WithHandshakeRecorder -> log-only, still accepts.
	resolver := NewStaticKeyResolver(map[string]string{"dev-key": "dev-tenant"})
	h := NewHandler(resolver, &fakeSink{}, nil)
	resp, err := h.Register(context.Background(), withBearer(&mapingv1.Handshake{Service: "s"}, "dev-key"))
	require.NoError(t, err)
	assert.True(t, resp.Msg.GetAccepted())
}
