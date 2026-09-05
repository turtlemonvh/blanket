package server

import "sync"

type EventHub struct {
	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

func NewEventHub() *EventHub {
	return &EventHub{subs: make(map[chan struct{}]struct{})}
}

func (h *EventHub) Subscribe() chan struct{} {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *EventHub) Unsubscribe(ch chan struct{}) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
}

// SubscriberCount reports how many live subscriptions the hub is holding.
// Exists for tests that need to assert a handler released its
// subscription on every exit path (notably the synchronous-wait handler's
// client-disconnect path, server/serve_sync.go); it's cheap enough to be
// useful for diagnostics too.
func (h *EventHub) SubscriberCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

func (h *EventHub) Notify() {
	h.mu.Lock()
	for ch := range h.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	h.mu.Unlock()
}
