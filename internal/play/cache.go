// Package play runs a compiled project and streams one turn of it.
//
// It owns the two pieces of state that outlive a single HTTP request: the
// compiled-project cache (MCP child processes, open session databases) and the
// pending-confirmation hub. Both have an explicit shutdown contract, which is the
// reason they live behind one Service instead of being fields on the HTTP server
// where any handler could reach them.
package play

import (
	"context"
	"fmt"
	"sync"
	"time"

	"google.golang.org/adk/v2/runner"

	"github.com/usherf233/adks/internal/store"
	"github.com/usherf233/adks/kit"
	"github.com/usherf233/adks/manifest"
)

const (
	// layerIdleTTL bounds how long a compiled project keeps its resources after
	// the last message. A compiled layer holds an MCP child process per stdio
	// server and an open session database, so an unbounded cache means a builder
	// that has opened twenty projects is running twenty child processes until it
	// exits. Long enough that switching between two projects stays instant.
	layerIdleTTL = 15 * time.Minute
	// closeTimeout bounds releasing one compiled layer.
	closeTimeout = 10 * time.Second
)

// layerCache keeps one compiled project alive between messages.
//
// Rebuilding per message would reconnect every MCP server and reopen the session
// database on each turn, which is both slow and, for stdio servers, a new child
// process per message. The cache is keyed by manifest hash, so an edit compiles
// on the next run and no earlier.
//
// Keeping entries forever is not an option: each one holds an MCP child process
// per stdio server plus an open session database. An idle entry is retired after
// ttl, which is why layerEntry.used has to be maintained rather than decorative.
type layerCache struct {
	st *store.Store

	mu      sync.Mutex
	entries map[string]*layerEntry
	locks   map[string]*sync.Mutex
	// ttl is the idle window after which a compiled project is released. Zero
	// disables eviction, which only tests want.
	ttl time.Duration
	// now is a seam so eviction can be tested without sleeping.
	now func() time.Time
}

type layerEntry struct {
	slug    string
	hash    string
	layer   *kit.Layer
	runner  *runner.Runner
	used    time.Time
	refs    int
	retired bool
}

func newLayerCache(st *store.Store) *layerCache {
	return &layerCache{
		st:      st,
		entries: map[string]*layerEntry{},
		locks:   map[string]*sync.Mutex{},
		ttl:     layerIdleTTL,
		now:     timeNow,
	}
}

func (c *layerCache) slugLock(slug string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	mu, ok := c.locks[slug]
	if !ok {
		mu = &sync.Mutex{}
		c.locks[slug] = mu
	}
	return mu
}

// nowTime reads the clock through the seam, falling back to the real one for a
// cache built by hand in a test.
func (c *layerCache) nowTime() time.Time {
	if c.now != nil {
		return c.now()
	}
	return timeNow()
}

// evictIdleLocked retires entries that have been idle past ttl. It must run with
// c.mu held. A dirty entry (refs > 0) is never taken: a save made mid-run must
// not pull the session database out from under that run.
func (c *layerCache) evictIdleLocked() {
	if c.ttl <= 0 {
		return
	}
	now := c.nowTime()
	var stale []*layerEntry
	for _, e := range c.entries {
		if e.refs == 0 && now.Sub(e.used) > c.ttl {
			stale = append(stale, e)
		}
	}
	for _, e := range stale {
		c.retireLocked(e)
	}
}

// Get returns a runner for the current manifest, compiling if needed. The
// returned release func must be called when the run finishes: entries carry a
// reference count so a save made mid-conversation cannot close the MCP
// connections and session database that run is still using.
func (c *layerCache) Get(ctx context.Context, slug string) (*runner.Runner, *manifest.Project, func(), error) {
	p, raw, err := c.st.Load(slug)
	if err != nil {
		return nil, nil, nil, err
	}
	hash := store.Hash(raw)

	mu := c.slugLock(slug)
	mu.Lock()
	defer mu.Unlock()

	c.mu.Lock()
	c.evictIdleLocked()
	entry, ok := c.entries[slug]
	if ok && entry.hash == hash && !entry.retired {
		entry.used = c.nowTime()
		entry.refs++
		release := c.releaseFunc(entry)
		c.mu.Unlock()
		return entry.runner, p, release, nil
	}
	if ok {
		c.retireLocked(entry)
	}
	c.mu.Unlock()

	rp, err := c.st.PrepareRuntime(p, slug)
	if err != nil {
		return nil, nil, nil, err
	}
	layer, err := kit.BuildWith(ctx, rp, kit.Options{
		AuditPath: c.st.RuntimeDir(slug) + "/audit.jsonl",
	})
	if err != nil {
		return nil, nil, nil, err
	}
	rnr, err := runner.New(runner.Config{
		AppName:           kit.AppName(rp),
		Agent:             layer.Agent,
		SessionService:    layer.Session,
		ArtifactService:   layer.Artifact,
		MemoryService:     layer.Memory,
		PluginConfig:      layer.Plugins,
		AutoCreateSession: true,
	})
	if err != nil {
		_ = layer.Close(ctx)
		return nil, nil, nil, fmt.Errorf("create runner: %w", err)
	}

	entry = &layerEntry{slug: slug, hash: hash, layer: layer, runner: rnr, used: c.nowTime(), refs: 1}
	c.mu.Lock()
	c.entries[slug] = entry
	c.mu.Unlock()
	return rnr, rp, c.releaseFunc(entry), nil
}

func (c *layerCache) releaseFunc(e *layerEntry) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			e.refs--
			// The idle clock starts when the last run ends, not when it began:
			// a two-minute conversation must not consume the whole TTL.
			e.used = c.nowTime()
			shouldClose := e.retired && e.refs == 0
			c.mu.Unlock()
			if shouldClose {
				go c.closeEntry(e)
			}
		})
	}
}

// retireLocked removes an entry from the cache. It must be called with c.mu held.
// An entry still in use is closed later, by the last release.
func (c *layerCache) retireLocked(e *layerEntry) {
	e.retired = true
	delete(c.entries, e.slug)
	if e.refs == 0 {
		go c.closeEntry(e)
	}
}

// Invalidate drops a compiled project. It is called on save and delete, so a
// removed project cannot keep serving from memory.
func (c *layerCache) Invalidate(slug string) {
	c.mu.Lock()
	if e, ok := c.entries[slug]; ok {
		c.retireLocked(e)
	}
	c.mu.Unlock()
}

// CloseAll releases every compiled project, including MCP child processes.
// Entries with active runs are closed by their last release; idle ones are
// closed here. It is only called at shutdown, so no new Get can race with it.
func (c *layerCache) CloseAll() {
	c.mu.Lock()
	entries := c.entries
	c.entries = map[string]*layerEntry{}
	// Per-slug locks are only created by Get; with the service shutting down they
	// can be released too instead of growing for every project ever opened.
	c.locks = map[string]*sync.Mutex{}
	idle := make([]*layerEntry, 0, len(entries))
	for _, e := range entries {
		e.retired = true
		if e.refs == 0 {
			idle = append(idle, e)
		}
	}
	c.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	for _, e := range idle {
		c.closeWith(ctx, e)
	}
}

func (c *layerCache) closeEntry(e *layerEntry) {
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	c.closeWith(ctx, e)
}

// closeWith is the only place a compiled layer is released. CloseAll uses it
// synchronously at shutdown; retired entries reach it through closeEntry.
func (c *layerCache) closeWith(ctx context.Context, e *layerEntry) {
	if e == nil || e.layer == nil {
		// An entry only exists once Build succeeded, but tests drive the cache
		// with placeholders: an unbuilt entry has nothing to release.
		return
	}
	_ = e.layer.Close(ctx)
}
