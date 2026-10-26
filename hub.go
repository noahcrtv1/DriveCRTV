package main

import (
	"encoding/json"
	"sync"
	"time"
)

// Hub distribui eventos (SSE) para as janelas abertas da interface.
type Hub struct {
	mu       sync.Mutex
	subs     map[chan []byte]struct{}
	lastZero time.Time
	started  time.Time
	ever     bool
}

func newHub() *Hub {
	return &Hub{subs: map[chan []byte]struct{}{}, lastZero: time.Now(), started: time.Now()}
}

func (h *Hub) subscribe() chan []byte {
	ch := make(chan []byte, 32)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.ever = true
	h.mu.Unlock()
	return ch
}

func (h *Hub) unsubscribe(ch chan []byte) {
	h.mu.Lock()
	delete(h.subs, ch)
	if len(h.subs) == 0 {
		h.lastZero = time.Now()
	}
	h.mu.Unlock()
}

func (h *Hub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// idle devolve ha quanto tempo nenhuma janela esta conectada e se alguma vez houve uma.
func (h *Hub) idle() (time.Duration, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.subs) > 0 {
		return 0, h.ever
	}
	return time.Since(h.lastZero), h.ever
}

func (h *Hub) broadcast(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	h.mu.Lock()
	for ch := range h.subs {
		select {
		case ch <- b:
		default: // cliente lento: descarta
		}
	}
	h.mu.Unlock()
}
