package buffer

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mapingv1 "github.com/arhuman/maping/proto/maping/v1"
)

// req builds a distinguishable UploadRequest tagged with a service name so
// tests can assert identity/order cheaply.
func req(tag string) *mapingv1.UploadRequest {
	return &mapingv1.UploadRequest{Envelope: &mapingv1.Envelope{Service: tag}}
}

func tags(reqs ...*mapingv1.UploadRequest) []string {
	out := make([]string, 0, len(reqs))
	for _, r := range reqs {
		if r == nil {
			out = append(out, "<nil>")
			continue
		}
		out = append(out, r.Envelope.Service)
	}
	return out
}

func TestNewClampsCapacity(t *testing.T) {
	tests := []struct {
		name string
		cap  int
		want int
	}{
		{"zero clamps to one", 0, 1},
		{"negative clamps to one", -5, 1},
		{"positive kept", 4, 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := New(tt.cap)
			assert.Len(t, r.buf, tt.want)
		})
	}
}

func TestPushFillWithoutOverflow(t *testing.T) {
	r := New(3)
	assert.Nil(t, r.Push(req("a")))
	assert.Nil(t, r.Push(req("b")))
	assert.Nil(t, r.Push(req("c")))
	assert.Equal(t, 3, r.Len())
	assert.Equal(t, "a", r.Peek().Envelope.Service, "oldest is first pushed")
}

func TestPushOverflowDropsOldestInOrder(t *testing.T) {
	r := New(2)
	require.Nil(t, r.Push(req("a")))
	require.Nil(t, r.Push(req("b")))

	// Ring full: pushing c evicts a (oldest), pushing d evicts b.
	assert.Equal(t, "a", r.Push(req("c")).Envelope.Service)
	assert.Equal(t, "b", r.Push(req("d")).Envelope.Service)
	assert.Equal(t, 2, r.Len())

	// Remaining, oldest-first: c then d.
	assert.Equal(t, []string{"c", "d"}, tags(r.PopOldest(), r.PopOldest()))
	assert.Equal(t, 0, r.Len())
}

func TestPushNilsOutEvictedSlot(t *testing.T) {
	r := New(2)
	r.Push(req("a"))
	r.Push(req("b"))

	// Overflowing pushes must not leave the evicted pointer pinned in buf.
	r.Push(req("c")) // evicts a
	r.Push(req("d")) // evicts b

	for i, slot := range r.buf {
		for _, dropped := range []string{"a", "b"} {
			if slot != nil {
				assert.NotEqual(t, dropped, slot.Envelope.Service,
					"evicted request %q still pinned at buf[%d]", dropped, i)
			}
		}
	}
}

func TestPopOldestNilsOutSlot(t *testing.T) {
	r := New(3)
	r.Push(req("a"))
	r.Push(req("b"))

	popped := r.PopOldest()
	require.Equal(t, "a", popped.Envelope.Service)

	// The vacated head slot must be niled out (no pinning of popped requests).
	nilCount := 0
	for _, slot := range r.buf {
		if slot == nil {
			nilCount++
		}
	}
	assert.Equal(t, 2, nilCount, "one slot vacated by pop, one never filled")
}

func TestPeekDoesNotRemove(t *testing.T) {
	r := New(2)
	r.Push(req("a"))
	r.Push(req("b"))

	assert.Equal(t, "a", r.Peek().Envelope.Service)
	assert.Equal(t, "a", r.Peek().Envelope.Service, "Peek is idempotent")
	assert.Equal(t, 2, r.Len(), "Peek must not remove")
}

func TestEmptyBehavior(t *testing.T) {
	r := New(2)
	assert.Nil(t, r.Peek(), "Peek on empty is nil")
	assert.Nil(t, r.PopOldest(), "PopOldest on empty is nil")
	assert.Equal(t, 0, r.Len())
}

func TestWrapAroundReuseAfterDrain(t *testing.T) {
	// Exercise head/tail wrap-around: fill, drain, refill past the physical end.
	r := New(3)
	r.Push(req("a"))
	r.Push(req("b"))
	r.Push(req("c"))
	require.Equal(t, "a", r.PopOldest().Envelope.Service)
	require.Equal(t, "b", r.PopOldest().Envelope.Service)

	// head has advanced; these pushes wrap around the backing array.
	r.Push(req("d"))
	r.Push(req("e"))
	assert.Equal(t, 3, r.Len())
	assert.Equal(t, []string{"c", "d", "e"}, tags(r.PopOldest(), r.PopOldest(), r.PopOldest()))
}
