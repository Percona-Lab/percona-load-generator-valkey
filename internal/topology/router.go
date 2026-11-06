package topology

import (
	"sync"
	"sync/atomic"

	"github.com/Percona-Lab/percona-load-generator-valkey/internal/valkey"
)

// MaxNodes bounds the node registry (per-node series are fixed-size).
const MaxNodes = 64

// Registry gives every node address a stable index, in the order first seen.
type Registry struct {
	mu    sync.Mutex
	addrs []string
	index map[string]int
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry { return &Registry{index: map[string]int{}} }

// Index returns the index of addr, adding it if new. It returns -1 once the
// registry is full.
func (r *Registry) Index(addr string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if i, ok := r.index[addr]; ok {
		return i
	}
	if len(r.addrs) >= MaxNodes {
		return -1
	}
	r.addrs = append(r.addrs, addr)
	r.index[addr] = len(r.addrs) - 1
	return len(r.addrs) - 1
}

// Addrs returns the addresses seen so far, by index.
func (r *Registry) Addrs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.addrs...)
}

// SlotRouter maps keys to the index (in a Registry) of the primary that owns
// their slot. It implements valkey.Router. Update replaces the map atomically.
type SlotRouter struct {
	reg   *Registry
	slots atomic.Pointer[[valkey.SlotCount]int16]
}

// NewSlotRouter creates a router and loads the first snapshot.
func NewSlotRouter(reg *Registry, s Snapshot) *SlotRouter {
	r := &SlotRouter{reg: reg}
	r.Update(s)
	return r
}

// Update rebuilds the slot map from a snapshot.
func (r *SlotRouter) Update(s Snapshot) {
	var m [valkey.SlotCount]int16
	for i := range m {
		m[i] = -1
	}
	for _, sh := range s.Shards {
		p, ok := sh.Primary()
		if !ok {
			continue
		}
		idx := int16(r.reg.Index(p.Addr))
		for _, rg := range sh.Slots {
			for slot := rg[0]; slot <= rg[1] && slot < valkey.SlotCount; slot++ {
				m[slot] = idx
			}
		}
	}
	r.slots.Store(&m)
}

// NodeIndex implements valkey.Router.
func (r *SlotRouter) NodeIndex(key string) int {
	m := r.slots.Load()
	if m == nil || key == "" {
		return -1
	}
	return int(m[valkey.Slot(key)])
}
