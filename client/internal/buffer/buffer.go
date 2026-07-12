// Package buffer implements a bounded, drop-oldest ring of pending
// UploadRequests for the mAPI-ng client's fail-open uploader (docs/context.md).
//
// A slow or down collector must never block the host: flushed windows are
// pushed into a fixed-capacity Ring, and the uploader goroutine drains the
// oldest pending request with retry/backoff. When the ring is full, the oldest
// request is evicted (drop-oldest) so the newest data always has room — losing
// stale windows is preferable to losing the freshest ones. The count of
// summaries in each evicted request is reported later via the envelope's
// dropped_summaries field so the loss is visible rather than silent.
//
// # Concurrency
//
// The Ring is single-goroutine-owned: only the uploader goroutine touches it
// (Push from the flush path runs on that same goroutine, Peek/PopOldest from
// the drain path likewise). It therefore carries no internal locking. Callers
// MUST NOT share a Ring across goroutines.
package buffer

import mapingv1 "github.com/arhuman/maping/proto/maping/v1"

// Ring is a fixed-capacity, drop-oldest ring buffer of *UploadRequest. It is
// not safe for concurrent use; it is owned by a single goroutine (see package
// doc). The zero value is not usable; construct one with New.
type Ring struct {
	buf   []*mapingv1.UploadRequest
	head  int // index of the oldest element
	count int // number of elements currently held
}

// New returns an empty Ring with the given capacity. A capacity below 1 is
// clamped to 1 so the ring can always hold at least the freshest request.
func New(capacity int) *Ring {
	if capacity < 1 {
		capacity = 1
	}
	return &Ring{buf: make([]*mapingv1.UploadRequest, capacity)}
}

// Push appends req as the newest element. If the ring is full it evicts and
// returns the oldest element (drop-oldest) so the caller can account for its
// dropped summaries; otherwise it returns nil. The evicted slot is niled out so
// the backing array never pins a dropped request (no memory leak).
func (r *Ring) Push(req *mapingv1.UploadRequest) *mapingv1.UploadRequest {
	var evicted *mapingv1.UploadRequest
	if r.count == len(r.buf) {
		evicted = r.buf[r.head]
		r.buf[r.head] = nil
		r.head = (r.head + 1) % len(r.buf)
		r.count--
	}
	tail := (r.head + r.count) % len(r.buf)
	r.buf[tail] = req
	r.count++
	return evicted
}

// Peek returns the oldest element without removing it, or nil if the ring is
// empty.
func (r *Ring) Peek() *mapingv1.UploadRequest {
	if r.count == 0 {
		return nil
	}
	return r.buf[r.head]
}

// PopOldest removes and returns the oldest element, or nil if the ring is
// empty. The vacated slot is niled out so the backing array never pins a
// removed request.
func (r *Ring) PopOldest() *mapingv1.UploadRequest {
	if r.count == 0 {
		return nil
	}
	req := r.buf[r.head]
	r.buf[r.head] = nil
	r.head = (r.head + 1) % len(r.buf)
	r.count--
	return req
}

// Len returns the number of elements currently held.
func (r *Ring) Len() int { return r.count }
