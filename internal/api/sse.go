package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/usherf233/adks/internal/play"
)

// sseWriter serialises writes to one event stream.
//
// Two goroutines write: the turn, and the keep-alive ticker. A ResponseWriter is
// not safe for concurrent use, and interleaved bytes would corrupt a frame, so
// every write goes through this lock.
type sseWriter struct {
	mu   sync.Mutex
	w    http.ResponseWriter
	f    http.Flusher
	enc  *json.Encoder
	dead bool
}

func newSSEWriter(w http.ResponseWriter, f http.Flusher) *sseWriter {
	return &sseWriter{w: w, f: f, enc: json.NewEncoder(w)}
}

// event writes one data frame. A failed write marks the stream dead, so a client
// that walked away does not produce a write error per remaining event.
func (s *sseWriter) event(ev play.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dead {
		return
	}
	if _, err := fmt.Fprint(s.w, "data: "); err != nil {
		s.dead = true
		return
	}
	if err := s.enc.Encode(ev); err != nil {
		s.dead = true
		return
	}
	if _, err := s.w.Write([]byte("\n")); err != nil {
		s.dead = true
		return
	}
	s.f.Flush()
}

// comment writes an SSE comment frame: clients ignore it, proxies count it as
// traffic and restart their read timeout.
func (s *sseWriter) comment(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dead {
		return
	}
	if _, err := fmt.Fprintf(s.w, ": %s\n\n", text); err != nil {
		s.dead = true
		return
	}
	s.f.Flush()
}

// keepAlive emits a comment frame every interval until stop closes.
//
// A turn can be silent for a long time: a slow tool call, or the two minutes spent
// waiting for a human to approve one. Our own server sets no write timeout, but a
// proxy in front does — nginx defaults to 60s — and it would drop a connection that
// is perfectly healthy. Clients ignore comments; the connection stays warm.
//
// done is closed on return, and the caller must wait for it before the handler
// ends: writing to a ResponseWriter after the handler returns panics.
func keepAlive(stop <-chan struct{}, done chan<- struct{}, w *sseWriter, every time.Duration) {
	defer close(done)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			w.comment("keep-alive")
		}
	}
}
