package dnsagent

import "sync"

// Per-server (name, type) → count reservoir, snapshot+reset on each
// metrics POST. Bounded so a unique-name query storm can't blow heap.
const (
	topNamesCap  = 5000
	topNamesShip = 100
)

type topK struct {
	mu     sync.Mutex
	bucket map[string]map[[2]string]int
}

func newTopK() *topK { return &topK{bucket: map[string]map[[2]string]int{}} }

// record increments the (name, qtype) counter for serverID. Drops new
// entries when the bucket is full so memory stays bounded.
func (t *topK) record(serverID, name, qtype string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	b, ok := t.bucket[serverID]
	if !ok {
		b = map[[2]string]int{}
		t.bucket[serverID] = b
	}
	key := [2]string{name, qtype}
	if _, present := b[key]; present {
		b[key]++
		return
	}
	if len(b) >= topNamesCap {
		return
	}
	b[key] = 1
}

// snapshot atomically resets the reservoir and returns the top-K
// entries by count. Reset-on-read matches the per-interval delta
// shape of the rcode counters.
func (t *topK) snapshot(serverID string) []topName {
	t.mu.Lock()
	b := t.bucket[serverID]
	delete(t.bucket, serverID)
	t.mu.Unlock()

	type kv struct {
		k     [2]string
		count int
	}
	all := make([]kv, 0, len(b))
	for k, c := range b {
		all = append(all, kv{k, c})
	}
	// partial sort would be cheaper at scale but len<5000 here
	for i := 1; i < len(all); i++ {
		for j := i; j > 0 && all[j].count > all[j-1].count; j-- {
			all[j], all[j-1] = all[j-1], all[j]
		}
	}
	if len(all) > topNamesShip {
		all = all[:topNamesShip]
	}
	out := make([]topName, len(all))
	for i, kv := range all {
		out[i] = topName{Name: kv.k[0], Type: kv.k[1], Count: kv.count}
	}
	return out
}

type topName struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Count int    `json:"count"`
}
