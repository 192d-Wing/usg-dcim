package main

import (
	"sort"
	"sync"
	"time"
)

// tracker is a thread-safe, fixed-capacity ring buffer of sample
// durations plus an error counter. report() returns a percentiles
// snapshot. Reservoir size 4096 is enough for stable p99 estimates
// at our scale (~1000 req/s) without unbounded memory.
type tracker struct {
	mu      sync.Mutex
	samples [4096]time.Duration
	n       int  // total observed (not bounded)
	idx     int  // next write position (wraps when n > cap)
	errors  int  // total non-2xx + transport errors
	bytes   int64 // total bytes sent (post-compression — informational only)
}

func (t *tracker) record(d time.Duration) {
	t.mu.Lock()
	t.samples[t.idx] = d
	t.idx = (t.idx + 1) % len(t.samples)
	t.n++
	t.mu.Unlock()
}

func (t *tracker) recordError() {
	t.mu.Lock()
	t.errors++
	t.n++
	t.mu.Unlock()
}

func (t *tracker) recordBytes(b int) {
	t.mu.Lock()
	t.bytes += int64(b)
	t.mu.Unlock()
}

type snapshot struct {
	n      int
	errors int
	bytes  int64
	p50    time.Duration
	p95    time.Duration
	p99    time.Duration
	max    time.Duration
}

// report returns a percentiles snapshot over the current reservoir
// contents. Sorts a copy so concurrent record() calls aren't blocked
// for the duration of the sort.
func (t *tracker) report() snapshot {
	t.mu.Lock()
	n := t.n
	if n > len(t.samples) {
		n = len(t.samples)
	}
	cp := make([]time.Duration, n)
	copy(cp, t.samples[:n])
	errors := t.errors
	bytes := t.bytes
	total := t.n
	t.mu.Unlock()

	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	pct := func(p float64) time.Duration {
		if len(cp) == 0 {
			return 0
		}
		i := int(float64(len(cp)-1) * p)
		return cp[i]
	}
	var maxD time.Duration
	if len(cp) > 0 {
		maxD = cp[len(cp)-1]
	}
	return snapshot{
		n:      total,
		errors: errors,
		bytes:  bytes,
		p50:    pct(0.50),
		p95:    pct(0.95),
		p99:    pct(0.99),
		max:    maxD,
	}
}
