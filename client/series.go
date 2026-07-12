package maping

import (
	"sync"
	"time"

	mapingv1 "github.com/arhuman/maping/proto/maping/v1"

	"github.com/arhuman/maping/client/sketch"
)

// maxStatusCodes bounds the exact-code breakdown per series (top-N guard
// against cardinality blowups from odd codes).
const maxStatusCodes = 20

// Record is the neutral, framework-agnostic input to Observe. An adapter builds
// one from a completed request.
type Record struct {
	Method        string
	RouteTemplate string
	Status        int
	Duration      time.Duration
	ReqBytes      int64
	RespBytes     int64
}

// classify buckets an HTTP status into a StatusClass series-key dimension. A
// zero or sub-100 status is NO_STATUS (aborted before a status was written).
func classify(status int) mapingv1.StatusClass {
	switch {
	case status <= 0 || status < 100:
		return mapingv1.StatusClass_STATUS_CLASS_NO_STATUS
	case status < 300:
		return mapingv1.StatusClass_STATUS_CLASS_2XX
	case status < 400:
		return mapingv1.StatusClass_STATUS_CLASS_3XX
	case status < 500:
		return mapingv1.StatusClass_STATUS_CLASS_4XX
	default:
		return mapingv1.StatusClass_STATUS_CLASS_5XX
	}
}

// seriesKey identifies one time series within a flush window. Service and
// instance are fixed per recorder and live on the Envelope, not the key.
type seriesKey struct {
	method string
	route  string
	class  mapingv1.StatusClass
}

// series is the in-window aggregate for one seriesKey.
type series struct {
	count         uint64
	sumDurationNs uint64
	reqBytes      uint64
	respBytes     uint64
	sk            *sketch.DDSketch
	codes         map[uint32]uint64 // bounded top-N exact codes
}

// shard is one lock-partitioned slice of the aggregation map. Observe touches
// only the shard its seriesKey hashes to, so N shards let up to N requests
// aggregate concurrently without contending on a single mutex.
type shard struct {
	mu sync.Mutex
	m  map[seriesKey]*series
}

// hash is a small FNV-1a over the seriesKey fields, computed without allocation
// so the hot path stays alloc-free. It ranges over the strings byte-by-byte
// (no []byte conversion) and folds in the status class.
func (k seriesKey) hash() uint32 {
	const (
		offset uint32 = 2166136261
		prime  uint32 = 16777619
	)
	h := offset
	for i := 0; i < len(k.method); i++ {
		h = (h ^ uint32(k.method[i])) * prime
	}
	for i := 0; i < len(k.route); i++ {
		h = (h ^ uint32(k.route[i])) * prime
	}
	c := uint32(k.class)
	h = (h ^ (c & 0xff)) * prime
	h = (h ^ ((c >> 8) & 0xff)) * prime
	return h
}
