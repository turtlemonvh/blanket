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

// SubscriberCount reports how many live subscriptions the hub holds, i.e.
// how many SSE streams or synchronous waiters are currently attached. Used by
// tests to assert that a handler released its subscription on every exit path
// (the SSE client-disconnect path, issue #103; the synchronous-wait handler in
// server/serve_sync.go, issue #27); cheap enough to be useful for diagnostics.
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
