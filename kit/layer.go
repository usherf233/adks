// Package kit compiles a manifest.Project into live ADK objects.
//
// It is the single compiler shared by two consumers: the adks builder imports it
// to try a project in-process, and an exported project receives a verbatim copy
// of this package's source so the two cannot drift. The package therefore may
// import only the standard library, ADK, jsonschema-go, gorm and manifest —
// never anything under internal/.
package kit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/cmd/launcher"
	"google.golang.org/adk/v2/memory"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"

	"github.com/adks-dev/adks/manifest"
)

// Layer is one compiled project: everything needed to run it, plus every
// resource that must be released when the process moves on.
//
// The zero value is not usable; obtain one from Build.
type Layer struct {
	Manifest *manifest.Project
	Agent    agent.Agent
	Loader   agent.Loader
	Session  session.Service
	Artifact artifact.Service
	Memory   memory.Service
	Plugins  runner.PluginConfig

	// Catalog is the tool surface the agent actually got. It is exposed for the
	// builder's preview and for tests: deferred loading replaces the flat list with
	// a single toolset, and that difference is invisible from the agent otherwise.
	Catalog Catalog

	// closers holds only objects whose Close releases a real resource (MCP
	// transport connections). Wrappers such as tool.FilterToolset are
	// deliberately excluded: closing them releases nothing.
	closers []io.Closer

	mu     sync.Mutex
	closed bool
}

// Catalog is the compiled tool surface.
type Catalog struct {
	Tools    []tool.Tool
	Toolsets []tool.Toolset
}

// AddCloser registers a resource to release on Layer.Close. It exists for the
// builder, which may attach its own resources (for example a probe connection)
// to a compiled layer.
func (l *Layer) AddCloser(c io.Closer) {
	if c == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closers = append(l.closers, c)
}

// Close releases registered resources in reverse order. It is idempotent and
// reports every error rather than stopping at the first one. The caller's context
// bounds the wait: a closer that blocks (a stuck stdio child, a locked sqlite
// file) must not hang shutdown forever. The closing goroutine may outlive a timed
// out call, but it is detached and holds no Layer state.
func (l *Layer) Close(ctx context.Context) error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	closers := l.closers
	l.closers = nil
	l.mu.Unlock()

	if len(closers) == 0 {
		return nil
	}
	done := make(chan error, 1)
	go func() {
		var errs []error
		for i := len(closers) - 1; i >= 0; i-- {
			if err := closers[i].Close(); err != nil {
				errs = append(errs, err)
			}
		}
		done <- errors.Join(errs...)
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// LauncherConfig adapts the layer to the ADK launcher, which is what an exported
// binary runs.
func (l *Layer) LauncherConfig() *launcher.Config {
	cfg := &launcher.Config{
		AgentLoader:     l.Loader,
		SessionService:  l.Session,
		ArtifactService: l.Artifact,
		MemoryService:   l.Memory,
		PluginConfig:    l.Plugins,
	}
	if l.Manifest != nil && l.Manifest.Spec.Runtime.Compaction != nil && l.Manifest.Spec.Runtime.Compaction.Enabled {
		cfg.Compaction = compactionConfig(l.Manifest.Spec.Runtime.Compaction)
	}
	return cfg
}

// AppName is the ADK application name: it namespaces sessions and artifacts, so
// it must be stable across restarts for a project.
func AppName(p *manifest.Project) string {
	if p == nil {
		return ""
	}
	if p.Metadata.Slug != "" {
		return p.Metadata.Slug
	}
	return fmt.Sprintf("adks-%s", p.Metadata.ID)
}
