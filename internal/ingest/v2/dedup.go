package ingestv2

import (
	"container/list"
	"sync"
)

const DefaultDedupCapacity = 65536

type dedupEntry struct {
	eventID string
}

// Dedup is an exact recent event_id LRU. It tracks newest-N inserts; Seen
// deliberately does not promote reads so replay order matches runtime order.
type Dedup struct {
	mu       sync.Mutex
	capacity int
	items    map[string]*list.Element
	order    *list.List
	size     interface{ Set(float64) }
}

// NewDedup creates an event_id LRU with a bounded capacity.
func NewDedup(capacity int, sizeGauge interface{ Set(float64) }) *Dedup {
	if capacity <= 0 {
		capacity = DefaultDedupCapacity
	}
	d := &Dedup{
		capacity: capacity,
		items:    make(map[string]*list.Element, capacity),
		order:    list.New(),
		size:     sizeGauge,
	}
	d.observeSizeLocked()
	return d
}

// Seen reports whether eventID is in the recent-ID cache.
func (d *Dedup) Seen(eventID string) bool {
	if d == nil || eventID == "" {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.items[eventID]
	return ok
}

// Add records eventID as the newest durable event and evicts oldest entries
// until the cache is within capacity.
func (d *Dedup) Add(eventID string) {
	if d == nil || eventID == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.addLocked(eventID)
}

// AddIfNew runs commit while holding the event-id cache lock, then records the
// ID only if commit succeeds. It returns true when the ID was already present.
func (d *Dedup) AddIfNew(eventID string, commit func() error) (bool, error) {
	if d == nil || eventID == "" {
		return false, commit()
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, ok := d.items[eventID]; ok {
		return true, nil
	}
	if err := commit(); err != nil {
		return false, err
	}
	d.addLocked(eventID)
	return false, nil
}

// Remove forgets eventID. It is used only to roll back a same-process dedupe
// mark when post-WAL projection fails before the event can be accepted.
func (d *Dedup) Remove(eventID string) {
	if d == nil || eventID == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	elem, ok := d.items[eventID]
	if !ok {
		return
	}
	delete(d.items, eventID)
	d.order.Remove(elem)
	d.observeSizeLocked()
}

func (d *Dedup) addLocked(eventID string) {
	if elem, ok := d.items[eventID]; ok {
		d.order.MoveToFront(elem)
		d.observeSizeLocked()
		return
	}
	elem := d.order.PushFront(dedupEntry{eventID: eventID})
	d.items[eventID] = elem
	for d.order.Len() > d.capacity {
		oldest := d.order.Back()
		if oldest == nil {
			break
		}
		entry := oldest.Value.(dedupEntry)
		delete(d.items, entry.eventID)
		d.order.Remove(oldest)
	}
	d.observeSizeLocked()
}

// Size returns the current number of tracked event IDs.
func (d *Dedup) Size() int {
	if d == nil {
		return 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.order.Len()
}

func (d *Dedup) observeSizeLocked() {
	if d.size != nil {
		d.size.Set(float64(d.order.Len()))
	}
}
