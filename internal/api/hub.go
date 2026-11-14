package api

import (
	"sync"
)

// playHub carries a human decision back to the streaming request that is waiting
// for it. Channels are registered when the confirmation request appears in the
// stream, so a fast click cannot arrive before anyone is listening.
type pendingDecision struct {
	ch      chan bool
	slug    string
	session string
}

type playHub struct {
	mu      sync.Mutex
	pending map[string]pendingDecision
}

func newPlayHub() *playHub { return &playHub{pending: map[string]pendingDecision{}} }

// owner is optional (slug, sessionID) so a decision can be bound to the run that
// asked for it. Tests that drive the hub directly may omit it.
func (h *playHub) register(callID string, owner ...string) chan bool {
	pd := pendingDecision{ch: make(chan bool, 1)}
	if len(owner) >= 2 {
		pd.slug, pd.session = owner[0], owner[1]
	}
	h.mu.Lock()
	h.pending[callID] = pd
	h.mu.Unlock()
	return pd.ch
}

// deliver accepts a decision only from the run that registered the call. A
// mismatched owner is refused without consuming the pending entry.
func (h *playHub) deliver(callID string, approved bool, owner ...string) bool {
	h.mu.Lock()
	pd, ok := h.pending[callID]
	if ok && len(owner) >= 2 && (pd.slug != owner[0] || pd.session != owner[1]) {
		h.mu.Unlock()
		return false
	}
	delete(h.pending, callID)
	h.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case pd.ch <- approved:
	default:
	}
	return true
}

func (h *playHub) drop(callID string) {
	h.mu.Lock()
	delete(h.pending, callID)
	h.mu.Unlock()
}

// Close drops decisions that will never be delivered. It is paired with
// layerCache.CloseAll so both of the server's stateful subsystems have an
// explicit shutdown contract.
func (h *playHub) Close() {
	h.mu.Lock()
	h.pending = map[string]pendingDecision{}
	h.mu.Unlock()
}
