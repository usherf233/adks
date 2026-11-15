package play

import (
	"context"
	"path/filepath"
	"time"

	"github.com/usherf233/adks/internal/store"
)

const (
	// confirmTimeout bounds how long a run waits for a human decision before it
	// reports the confirmation as timed out and continues without the tool.
	confirmTimeout = 120 * time.Second
	// defaultWallClock is the per-turn budget when the manifest does not set one.
	defaultWallClock = 120 * time.Second
)

// timeNow is a seam for tests.
var timeNow = time.Now

// Service runs trial turns for the builder.
type Service struct {
	st    *store.Store
	cache *layerCache
	hub   *playHub
	stats *statSink
}

// New wires the service. The returned value owns MCP child processes and open
// session databases, so Close must be called on shutdown.
func New(st *store.Store) *Service {
	return &Service{st: st, cache: newLayerCache(st), hub: newPlayHub(), stats: &statSink{}}
}

// Prepare compiles the project if needed and pins it for one turn.
//
// A failure here is a setup failure and happens before anything is streamed,
// which is what lets the HTTP layer answer 404 for a missing project and 400 for
// a manifest that does not compile. Once Converse starts, events only.
func (s *Service) Prepare(ctx context.Context, slug string) (*Session, error) {
	rnr, p, release, err := s.cache.Get(ctx, slug)
	if err != nil {
		return nil, err
	}
	return &Session{
		slug:      slug,
		rnr:       rnr,
		project:   p,
		hub:       s.hub,
		release:   release,
		statsPath: filepath.Join(s.st.RuntimeDir(slug), "stats.jsonl"),
		stats:     s.stats,
	}, nil
}

// Confirm delivers a human decision to the run that asked for it. It returns
// false when nothing is waiting, which is not an error: the run may already have
// timed out.
func (s *Service) Confirm(slug, sessionID, callID string, approved bool) bool {
	return s.hub.deliver(callID, approved, slug, sessionID)
}

// Invalidate drops a compiled project after its manifest changed or it was
// deleted, so the next turn recompiles instead of serving stale state.
func (s *Service) Invalidate(slug string) {
	s.cache.Invalidate(slug)
}

// Close releases every compiled project and drops pending confirmations.
func (s *Service) Close() {
	s.cache.CloseAll()
	s.hub.Close()
}
