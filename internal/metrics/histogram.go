// Package metrics provides a small rolling percentile histogram used for
// end-to-end sync latency.
package metrics

import (
	"math"
	"sort"
	"sync"
)

// Histogram keeps a bounded, time-ordered ring of samples and computes
// percentiles on demand. Old samples are dropped (ring overwrite) so memory
// stays bounded for 24h+ runs.
type Histogram struct {
	mu      sync.Mutex
	buf     []float64
	idx     int
	wrapped bool
	count   uint64
	sum     float64
}

// NewHistogram returns a histogram that retains at most cap samples.
func NewHistogram(cap int) *Histogram {
	if cap < 4 {
		cap = 4
	}
	return &Histogram{buf: make([]float64, 0, cap), idx: cap - 1}
}

// Add records a sample.
func (h *Histogram) Add(v float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.wrapped && len(h.buf) < cap(h.buf) {
		h.buf = append(h.buf, v)
	} else {
		if !h.wrapped {
			h.wrapped = true
		}
		h.idx = (h.idx + 1) % cap(h.buf)
		h.buf[h.idx] = v
	}
	h.count++
	h.sum += v
}

// Summary is a point-in-time percentile snapshot.
type Summary struct {
	Count uint64  `json:"count"`
	Sum   float64 `json:"sum"`
	P50   float64 `json:"p50"`
	P95   float64 `json:"p95"`
	P99   float64 `json:"p99"`
	Max   float64 `json:"max"`
}

// Percentile returns the p-th percentile (0..100) of retained samples, or
// NaN when there are no samples.
func (h *Histogram) Percentile(p float64) float64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.buf) == 0 {
		return math.NaN()
	}
	s := make([]float64, len(h.buf))
	copy(s, h.buf)
	sort.Float64s(s)
	idx := int(math.Ceil(p/100*float64(len(s)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(s) {
		idx = len(s) - 1
	}
	return s[idx]
}

// Summary returns a full percentile snapshot.
func (h *Histogram) Summary() Summary {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.buf) == 0 {
		return Summary{Count: h.count, Sum: h.sum}
	}
	s := make([]float64, len(h.buf))
	copy(s, h.buf)
	sort.Float64s(s)
	return Summary{
		Count: h.count,
		Sum:   h.sum,
		P50:   pct(s, 50),
		P95:   pct(s, 95),
		P99:   pct(s, 99),
		Max:   s[len(s)-1],
	}
}

// Count returns the total number of recorded samples.
func (h *Histogram) Count() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.count
}

func pct(s []float64, p float64) float64 {
	idx := int(math.Ceil(p/100*float64(len(s)))) - 1
	if idx < 0 {
		idx = 0
	}
	return s[idx]
}
