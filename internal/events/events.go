// Package events is Dupearr's in-process publish/subscribe bus. It feeds the Server-Sent Events
// stream (/api/v1/events) and anything else that wants to observe state changes.
//
// Publishing never blocks: every subscriber owns a buffered channel and an event is dropped for a
// subscriber whose buffer is full (a slow SSE client must not stall a scan).
package events

import "sync"

// Event names (resources) published on the bus.
const (
	NameDuplicate = "duplicate"
	NameQueue     = "queue"
	NameHistory   = "history"
	NameCommand   = "command"
	NameHealth    = "health"
	NameTask      = "task"
	NameScan      = "scan"
	NameSettings  = "settings"
)

// Event actions.
const (
	ActionUpdated  = "updated"
	ActionDeleted  = "deleted"
	ActionSync     = "sync"
	ActionProgress = "progress"
)

// DefaultBuffer is the subscriber channel capacity used when Subscribe is called with buffer ≤ 0.
const DefaultBuffer = 64

// Event is one message on the bus (serialized as-is to SSE clients).
type Event struct {
	Name     string `json:"name"`   // resource: "duplicate","queue","history","command","health","task","scan","settings"
	Action   string `json:"action"` // "updated","deleted","sync","progress"
	Resource any    `json:"resource,omitempty"`
}

// Bus is a fan-out event bus. The zero value is not usable; call New. A nil *Bus is a valid no-op
// bus (Publish does nothing, Subscribe returns a closed channel), which keeps tests simple.
type Bus struct {
	mu     sync.RWMutex
	nextID uint64
	subs   map[uint64]chan Event
}

// New returns an empty bus.
func New() *Bus {
	return &Bus{subs: make(map[uint64]chan Event)}
}

// Publish delivers e to every current subscriber without blocking; subscribers whose buffer is
// full miss the event.
func (b *Bus) Publish(e Event) {
	if b == nil {
		return
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs {
		select {
		case ch <- e:
		default: // slow subscriber: drop rather than block the publisher
		}
	}
}

// Subscribe registers a subscriber with the given channel buffer (≤0 → DefaultBuffer) and returns
// its receive channel plus an unsubscribe func. Unsubscribe is idempotent and closes the channel.
func (b *Bus) Subscribe(buffer int) (<-chan Event, func()) {
	if buffer <= 0 {
		buffer = DefaultBuffer
	}
	ch := make(chan Event, buffer)
	if b == nil {
		close(ch)
		return ch, func() {}
	}

	b.mu.Lock()
	id := b.nextID
	b.nextID++
	b.subs[id] = ch
	b.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			// Taking the write lock guarantees no Publish is mid-send on ch when it is closed.
			b.mu.Lock()
			delete(b.subs, id)
			b.mu.Unlock()
			close(ch)
		})
	}
	return ch, unsubscribe
}

// Subscribers returns the current number of subscribers.
func (b *Bus) Subscribers() int {
	if b == nil {
		return 0
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}
