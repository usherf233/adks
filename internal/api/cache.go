package api

import (
	"context"
	"fmt"
	"sync"
	"time"

	"google.golang.org/adk/v2/runner"

	"github.com/adks-dev/adks/internal/store"
	"github.com/adks-dev/adks/kit"
	"github.com/adks-dev/adks/manifest"
)

// layerCache keeps one compiled project alive between messages.
//
// Rebuilding per message would reconnect every MCP server and reopen the session
// database on each turn, which is both slow and, for stdio servers, a new child
// process per message. The cache is keyed by manifest hash, so an edit compiles
// on the next run and no earlier.
type layerCache struct {
	st *store.Store

	mu      sync.Mutex
	entries map[string]*layerEntry
	locks   map[string]*sync.Mutex
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
	return &layerCache{st: st, entries: map[string]*layerEntry{}, locks: map[string]*sync.Mutex{}}
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
	entry, ok := c.entries[slug]
	if ok && entry.hash == hash && !entry.retired {
		entry.used = timeNow()
		entry.refs++
		release := c.releaseFunc(entry)
		c.mu.Unlock()
		return entry.runner, p, release, nil
	}
	if ok {
		c.retireLocked(entry)
	}
	c.mu.Unlock()

	runtimeDir := c.st.RuntimeDir(slug)
	rp, err := runtimeProject(p, runtimeDir)
	if err != nil {
		return nil, nil, nil, err
	}
	layer, err := kit.BuildWith(ctx, rp, kit.Options{
		AuditPath: runtimeDir + "/audit.jsonl",
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

	entry = &layerEntry{slug: slug, hash: hash, layer: layer, runner: rnr, used: timeNow(), refs: 1}
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
	// Per-slug locks are only created by Get; with the server shutting down they
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
	_ = e.layer.Close(ctx)
}
