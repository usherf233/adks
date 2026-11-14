package kit

import (
	"fmt"
	"os"
	"regexp"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/plugin"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session/compaction"
	"google.golang.org/adk/v2/tool"

	"github.com/adks-dev/adks/manifest"
)

// multiToolset merges several toolsets into one, preserving the order it was
// given. Deferred loading needs a single base toolset even when the catalog is
// made of several MCP servers plus a list of plain tools.
type multiToolset struct {
	name     string
	toolsets []tool.Toolset
}

func (m multiToolset) Name() string { return m.name }

func (m multiToolset) Tools(ctx agent.ReadonlyContext) ([]tool.Tool, error) {
	var out []tool.Tool
	for _, ts := range m.toolsets {
		tools, err := ts.Tools(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, tools...)
	}
	return out, nil
}

// asInt accepts the numeric shapes a tool result can carry after the framework
// round-trips it through JSON: encoding turns every number into a float64, so a
// plain type assertion to int silently fails and the audit log loses the status.
func asInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case float32:
		return int(n)
	}
	return 0
}

var reEnvRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandEnv resolves ${VAR} references. A missing variable is an error rather
// than an empty string: an MCP server that silently receives an empty token fails
// later with a confusing 401.
func expandEnv(s string) (string, error) {
	var missing string
	out := reEnvRef.ReplaceAllStringFunc(s, func(m string) string {
		name := m[2 : len(m)-1]
		v := os.Getenv(name)
		if v == "" && missing == "" {
			missing = name
		}
		return v
	})
	if missing != "" {
		return "", fmt.Errorf("environment variable %s is empty", missing)
	}
	return out, nil
}

// runnerPluginConfig wraps the plugin list for the runner.
func runnerPluginConfig(plugins []*plugin.Plugin) runner.PluginConfig {
	if len(plugins) == 0 {
		return runner.PluginConfig{}
	}
	return runner.PluginConfig{Plugins: plugins, CloseTimeout: 5 * time.Second}
}

// compactionConfig maps the manifest knob onto ADK's config.
//
// The manifest exposes only an on/off switch; the summarizer model field is
// reserved for a later version. ADK's sliding window needs an interval, and six
// invocations is a value chosen to keep
// the summarized span small enough that losing one window does not lose the task.
// Tail retention (token threshold) stays off, because it summarizes mid-turn and
// a wrong summary there is indistinguishable from the model forgetting.
func compactionConfig(spec *manifest.CompactionSpec) *compaction.Config {
	if spec == nil || !spec.Enabled {
		return nil
	}
	return &compaction.Config{CompactionInterval: 6}
}

// expandHeaders resolves ${VAR} in header values at build time so the expanded
// secret never sits in the manifest, a log line, or the generated source.
func expandHeaders(in map[string]string) (map[string]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		expanded, err := expandEnv(v)
		if err != nil {
			return nil, fmt.Errorf("mcp header %q: %w", k, err)
		}
		out[k] = expanded
	}
	return out, nil
}
