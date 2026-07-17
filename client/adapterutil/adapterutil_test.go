package adapterutil

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	maping "github.com/arhuman/maping/client"
)

func TestParseTraceparent(t *testing.T) {
	tests := []struct {
		name      string
		header    string
		wantTrace string
		wantSpan  string
	}{
		{
			name:      "valid",
			header:    "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			wantTrace: "4bf92f3577b34da6a3ce929d0e0e4736",
			wantSpan:  "00f067aa0ba902b7",
		},
		{
			name:      "valid uppercase hex",
			header:    "00-4BF92F3577B34DA6A3CE929D0E0E4736-00F067AA0BA902B7-01",
			wantTrace: "4BF92F3577B34DA6A3CE929D0E0E4736",
			wantSpan:  "00F067AA0BA902B7",
		},
		{name: "absent", header: "", wantTrace: "", wantSpan: ""},
		{name: "too few parts", header: "00-abc-def", wantTrace: "", wantSpan: ""},
		{name: "too many parts", header: "00-a-b-c-d", wantTrace: "", wantSpan: ""},
		{
			name:   "short trace id",
			header: "00-4bf9-00f067aa0ba902b7-01",
		},
		{
			name:   "non-hex trace id",
			header: "00-zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz-00f067aa0ba902b7-01",
		},
		{
			name:   "all-zero trace id (invalid sentinel)",
			header: "00-00000000000000000000000000000000-00f067aa0ba902b7-01",
		},
		{
			name:   "all-zero span id (invalid sentinel)",
			header: "00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01",
		},
		{
			name:   "short span id",
			header: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f0-01",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotTrace, gotSpan := ParseTraceparent(tt.header)
			assert.Equal(t, tt.wantTrace, gotTrace)
			assert.Equal(t, tt.wantSpan, gotSpan)
		})
	}
}

// TestClampNonNegative covers both branches of the clamping helper.
func TestClampNonNegative(t *testing.T) {
	tests := []struct {
		name string
		in   int64
		want int64
	}{
		{"zero", 0, 0},
		{"positive", 42, 42},
		{"negative (unknown length)", -1, 0},
		{"large negative", -100, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ClampNonNegative(tt.in))
		})
	}
}

// TestReclassifyNoStatus covers each cancellation cause path: a live context
// keeps the written status, while a canceled, deadline-fired, or custom-cause
// context is reclassified to NO_STATUS with the matching reason.
func TestReclassifyNoStatus(t *testing.T) {
	t.Run("live context keeps status", func(t *testing.T) {
		status, reason := ReclassifyNoStatus(context.Background(), 200)
		assert.Equal(t, 200, status)
		assert.Equal(t, maping.NoStatusUnspecified, reason)
	})

	t.Run("canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		status, reason := ReclassifyNoStatus(ctx, 200)
		assert.Equal(t, 0, status)
		assert.Equal(t, maping.NoStatusContextCanceled, reason)
	})

	t.Run("deadline fired", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		status, reason := ReclassifyNoStatus(ctx, 200)
		assert.Equal(t, 0, status)
		assert.Equal(t, maping.NoStatusContextDeadline, reason)
	})

	t.Run("custom cause folds to other", func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(errors.New("boom"))
		status, reason := ReclassifyNoStatus(ctx, 200)
		assert.Equal(t, 0, status)
		assert.Equal(t, maping.NoStatusOther, reason)
	})
}
