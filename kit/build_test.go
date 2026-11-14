package kit

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/adks-dev/adks/manifest"
)

func testProject(t *testing.T) *manifest.Project {
	t.Helper()
	p := manifest.New("demo", "Demo")
	p.Spec.Agent.Instruction = "You answer questions using the tools you are given."
	p.Spec.Tools.Builtin = []string{"google_search", "exit_loop"}
	p.Spec.Tools.HTTP = []manifest.HTTPSpec{
		{
			Name: "zzz_last", Description: "this tool exists to check the sort order",
			Method: "GET", URL: "https://api.example.com/z", AllowedHosts: []string{"api.example.com"},
		},
		{
			Name: "aaa_first", Description: "this tool exists to check the sort order",
			Method: "GET", URL: "https://api.example.com/a", AllowedHosts: []string{"api.example.com"},
		},
	}
	p.Spec.Runtime.Session = manifest.SessionSpec{Backend: "sqlite", DSN: filepath.Join(t.TempDir(), "s.db"), AutoMigrate: true}
	p.Spec.Runtime.Artifact = manifest.ArtifactSpec{Backend: "local", Dir: t.TempDir()}
	p.Spec.Plugins = []manifest.PluginRef{{Name: "logging", Kind: "builtin.logging"}}
	manifest.ApplyDefaults(p)
	if issues := manifest.Validate(p); len(issues) > 0 {
		for _, i := range issues {
			if i.Level == manifest.LevelError {
				t.Fatalf("test manifest invalid: %v", issues)
			}
		}
	}
	return p
}

// TestBuildAssemblesWithoutCredentials compiles the project with the model check
// skipped, which is what the builder's static verify path does. It proves tool
// assembly, service construction and plugin wiring do not depend on a live model.
func TestBuildAssemblesWithoutCredentials(t *testing.T) {
	p := testProject(t)
	layer, err := BuildWith(context.Background(), p, Options{SkipModelCheck: true})
	if err != nil {
		t.Fatalf("BuildWith: %v", err)
	}
	defer func() {
		if err := layer.Close(context.Background()); err != nil {
			t.Errorf("close: %v", err)
		}
	}()

	if layer.Agent == nil || layer.Loader == nil {
		t.Fatal("agent and loader must be set")
	}
	if layer.Session == nil || layer.Artifact == nil || layer.Memory == nil {
		t.Fatal("all three services must be set")
	}
	if len(layer.Plugins.Plugins) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(layer.Plugins.Plugins))
	}

	cfg := layer.LauncherConfig()
	if cfg.Compaction != nil {
		t.Fatal("compaction must stay nil unless the manifest enables it")
	}
}

// TestBuildRefusesInvalidManifest keeps the compiler from silently accepting a
// manifest the editor would have flagged.
func TestBuildRefusesInvalidManifest(t *testing.T) {
	p := testProject(t)
	p.Spec.Agent.Name = "user" // reserved
	if _, err := BuildWith(context.Background(), p, Options{SkipModelCheck: true}); err == nil {
		t.Fatal("expected build to fail on an invalid manifest")
	}
}

// TestBuildIsIdempotentOnClose verifies that a second Close is a no-op, because
// both the deferred close and the cache invalidation may run.
func TestBuildIsIdempotentOnClose(t *testing.T) {
	p := testProject(t)
	layer, err := BuildWith(context.Background(), p, Options{SkipModelCheck: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := layer.Close(context.Background()); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := layer.Close(context.Background()); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

// TestBuildWithDeferredToolLoading covers the toggle that changes how tools reach
// the prompt: with toolSearch on, the agent gets one search toolset instead of a
// flat list, and a build that silently kept the flat list would still work while
// costing every tool's schema on every turn.
func TestBuildWithDeferredToolLoading(t *testing.T) {
	p := testProject(t)
	p.Spec.Tools.ToolSearch = true
	// toolsearch needs a few tools to be worth it; the test project has four.
	p.Spec.Tools.Builtin = []string{"google_search", "exit_loop"}

	layer, err := BuildWith(context.Background(), p, Options{SkipModelCheck: true})
	if err != nil {
		t.Fatalf("BuildWith: %v", err)
	}
	defer layer.Close(context.Background())

	if len(layer.Catalog.Tools) != 0 {
		t.Fatalf("deferred loading must replace the flat list, got %d tools", len(layer.Catalog.Tools))
	}
	if len(layer.Catalog.Toolsets) != 1 {
		t.Fatalf("expected exactly one toolset, got %d", len(layer.Catalog.Toolsets))
	}
	tools, err := layer.Catalog.Toolsets[0].Tools(probeContext{context.Background()})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	var names []string
	for _, tl := range tools {
		names = append(names, tl.Name())
	}
	if len(names) > 2 {
		t.Fatalf("deferred loading should disclose only the search tool and cores, got %v", names)
	}
	if layer.Agent.Name() != p.Spec.Agent.Name {
		t.Fatalf("agent name changed: %q", layer.Agent.Name())
	}
}

// TestBuildKeepsFlatCatalog checks the default path: no toolSearch means every
// tool is disclosed up front.
func TestBuildKeepsFlatCatalog(t *testing.T) {
	p := testProject(t)
	layer, err := BuildWith(context.Background(), p, Options{SkipModelCheck: true})
	if err != nil {
		t.Fatal(err)
	}
	defer layer.Close(context.Background())

	if len(layer.Catalog.Toolsets) != 0 {
		t.Fatalf("expected no toolsets, got %d", len(layer.Catalog.Toolsets))
	}
	if len(layer.Catalog.Tools) != 4 {
		t.Fatalf("expected 4 tools (2 builtin + 2 http), got %d", len(layer.Catalog.Tools))
	}
	// Sorted by name so the prompt prefix is stable regardless of manifest order.
	for i := 1; i < len(layer.Catalog.Tools); i++ {
		if layer.Catalog.Tools[i-1].Name() > layer.Catalog.Tools[i].Name() {
			t.Fatalf("tools not sorted: %q before %q",
				layer.Catalog.Tools[i-1].Name(), layer.Catalog.Tools[i].Name())
		}
	}
}
