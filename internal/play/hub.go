package play

import "sync"

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

// register binds a call to the run that asked for it.
func (h *playHub) register(callID, slug, sessionID string) chan bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	pd := pendingDecision{ch: make(chan bool, 1), slug: slug, session: sessionID}
	h.pending[callID] = pd
	return pd.ch
}

// deliver accepts a decision only from the run that registered the call. A
// mismatched owner is refused without consuming the pending entry.
func (h *playHub) deliver(callID string, approved bool, slug, sessionID string) bool {
	h.mu.Lock()
	pd, ok := h.pending[callID]
	if ok && (pd.slug != slug || pd.session != sessionID) {
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
// layerCache.CloseAll so both of the service's stateful parts have an explicit
// shutdown contract.
func (h *playHub) Close() {
	h.mu.Lock()
	h.pending = map[string]pendingDecision{}
	h.mu.Unlock()
}
