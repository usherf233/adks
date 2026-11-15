package play

import (
	"sync"
	"testing"
	"time"

	"github.com/usherf233/adks/internal/store"
)

func idleCache(t *testing.T, ttl time.Duration) (*layerCache, *time.Time) {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	c := &layerCache{
		st:      st,
		entries: map[string]*layerEntry{},
		locks:   map[string]*sync.Mutex{},
		ttl:     ttl,
		now:     func() time.Time { return clock },
	}
	return c, &clock
}

// TestEvictIdleRetiresStaleEntry is the behaviour the cache was missing: an idle
// compiled project must not keep an MCP child process and a session handle open
// for the lifetime of the builder.
func TestEvictIdleRetiresStaleEntry(t *testing.T) {
	c, clock := idleCache(t, time.Minute)
	e := &layerEntry{slug: "demo", used: *clock}
	c.entries["demo"] = e

	*clock = clock.Add(2 * time.Minute)
	c.mu.Lock()
	c.evictIdleLocked()
	c.mu.Unlock()

	if _, ok := c.entries["demo"]; ok {
		t.Fatal("an entry idle past the TTL is still cached")
	}
	if !e.retired {
		t.Fatal("the evicted entry was not marked retired")
	}
}

// TestEvictIdleKeepsWarmEntry guards the other side: a project used a moment ago
// must stay compiled, or every message reconnects every MCP server.
func TestEvictIdleKeepsWarmEntry(t *testing.T) {
	c, clock := idleCache(t, time.Minute)
	c.entries["demo"] = &layerEntry{slug: "demo", used: *clock}

	*clock = clock.Add(30 * time.Second)
	c.mu.Lock()
	c.evictIdleLocked()
	c.mu.Unlock()

	if _, ok := c.entries["demo"]; !ok {
		t.Fatal("a warm entry was evicted")
	}
}

// TestEvictIdleNeverTakesRunningEntry is the safety rule: eviction must not pull
// a session database out from under a run that is still streaming.
func TestEvictIdleNeverTakesRunningEntry(t *testing.T) {
	c, clock := idleCache(t, time.Minute)
	e := &layerEntry{slug: "demo", used: *clock, refs: 1}
	c.entries["demo"] = e

	*clock = clock.Add(time.Hour)
	c.mu.Lock()
	c.evictIdleLocked()
	c.mu.Unlock()

	if _, ok := c.entries["demo"]; !ok {
		t.Fatal("an entry with an active run was evicted")
	}
	if e.retired {
		t.Fatal("an entry with an active run was marked retired")
	}
}

// TestReleaseRestartsIdleClock pins that the TTL is measured from the end of a
// run: a long conversation would otherwise be evicted the moment it finishes.
func TestReleaseRestartsIdleClock(t *testing.T) {
	c, clock := idleCache(t, time.Minute)
	e := &layerEntry{slug: "demo", used: *clock, refs: 1}
	c.entries["demo"] = e

	*clock = clock.Add(5 * time.Minute)
	release := c.releaseFunc(e)
	release()

	if got := e.used; !got.Equal(*clock) {
		t.Fatalf("release did not restart the idle clock: used=%v now=%v", got, *clock)
	}
	c.mu.Lock()
	c.evictIdleLocked()
	c.mu.Unlock()
	if _, ok := c.entries["demo"]; !ok {
		t.Fatal("the entry was evicted immediately after its run ended")
	}
}

// TestEvictDisabledWhenTTLZero keeps the test-only escape hatch honest: a zero
// TTL must never silently behave like a very short one.
func TestEvictDisabledWhenTTLZero(t *testing.T) {
	c, clock := idleCache(t, 0)
	c.entries["demo"] = &layerEntry{slug: "demo", used: *clock}

	*clock = clock.Add(24 * time.Hour)
	c.mu.Lock()
	c.evictIdleLocked()
	c.mu.Unlock()

	if _, ok := c.entries["demo"]; !ok {
		t.Fatal("eviction ran with a zero TTL")
	}
}
