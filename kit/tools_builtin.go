package kit

import (
	"fmt"
	"sort"

	"google.golang.org/adk/v2/agent"

	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/exitlooptool"
	"google.golang.org/adk/v2/tool/geminitool"
	"google.golang.org/adk/v2/tool/loadartifactstool"
	"google.golang.org/adk/v2/tool/preloadmemorytool"
)

// builtinTools maps a manifest builtin name to its constructor. The set is
// closed: anything else is a validation error, so this map and
// manifest.BuiltinToolNames must stay in sync (TestBuiltinToolsMatchManifest).
var builtinTools = map[string]func() (tool.Tool, error){
	"google_search":  func() (tool.Tool, error) { return geminitool.GoogleSearch{}, nil },
	"url_context":    func() (tool.Tool, error) { return geminitool.UrlContext{}, nil },
	"exit_loop":      exitlooptool.New,
	"load_artifacts": func() (tool.Tool, error) { return loadartifactstool.New(), nil },
	"preload_memory": func() (tool.Tool, error) { return preloadmemorytool.New(), nil },
}

func newBuiltinTool(name string) (tool.Tool, error) {
	factory, ok := builtinTools[name]
	if !ok {
		return nil, fmt.Errorf("unknown builtin tool %q", name)
	}
	t, err := factory()
	if err != nil {
		return nil, fmt.Errorf("build builtin tool %q: %w", name, err)
	}
	return t, nil
}

// sortTools orders tools by name. The order reaches the model as part of the
// prompt prefix, so it must not depend on manifest order or map iteration.
func sortTools(tools []tool.Tool) {
	sort.SliceStable(tools, func(i, j int) bool { return tools[i].Name() < tools[j].Name() })
}

// staticToolset adapts a fixed list of tools to tool.Toolset. Deferred loading
// (tool/toolsearch) needs a toolset even when every tool is a plain tool.Tool.
type staticToolset struct {
	name  string
	tools []tool.Tool
}

func (s staticToolset) Name() string { return s.name }

func (s staticToolset) Tools(_ agent.ReadonlyContext) ([]tool.Tool, error) {
	return s.tools, nil
}
