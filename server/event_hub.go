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
