package kit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/toolsearch"

	"github.com/adks-dev/adks/manifest"
)

// Options tunes a build. The zero value is the production default.
type Options struct {
	// AuditPath, when set, receives one JSON line per tool call.
	AuditPath string
	// AuditVerbose includes raw tool arguments in the audit log. Off by default.
	AuditVerbose bool
	// HTTPClient overrides the shared client of the HTTP tools.
	HTTPClient *http.Client
	// Secrets pre-resolves credentials. Missing keys fall back to the process
	// environment.
	Secrets map[string]string
	// SkipModelCheck lets a caller compile a project without a model API key,
	// which the static verifier needs: it wants the generated code to build, not
	// to call a provider.
	SkipModelCheck bool
}

// Build compiles a manifest. Convenience wrapper for the zero Options.
func Build(ctx context.Context, p *manifest.Project) (*Layer, error) {
	return BuildWith(ctx, p, Options{})
}

// BuildWith compiles a manifest into a Layer.
//
// Order is fixed: validate, model, tools (sorted), callbacks, agent, services,
// plugins. A failure at any step releases what earlier steps created, because the
// MCP toolsets hold live child processes and the SQL session holds a connection
// pool.
func BuildWith(ctx context.Context, p *manifest.Project, opts Options) (*Layer, error) {
	if p == nil {
		return nil, errors.New("kit: manifest is nil")
	}
	if issues := manifest.Validate(p); manifest.HasErrors(issues) {
		var errs []string
		for _, i := range issues {
			if i.Level == manifest.LevelError {
				errs = append(errs, i.String())
			}
		}
		return nil, fmt.Errorf("kit: manifest has %d error(s):\n  %s", len(errs), strings.Join(errs, "\n  "))
	}

	l := &Layer{Manifest: p}
	ok := false
	defer func() {
		if !ok {
			_ = l.Close(ctx)
		}
	}()

	llm, err := newModel(ctx, p.Spec.Agent.Model)
	if err != nil {
		if !opts.SkipModelCheck {
			return nil, err
		}
		// The static verifier path: the model object is never used, so a dummy is
		// unnecessary — but llmagent.New requires a non-nil model.
		llm = &unavailableModel{reason: err.Error()}
	}

	tools, toolsets, closers, err := buildTools(p, opts)
	if err != nil {
		return nil, err
	}
	for _, c := range closers {
		l.AddCloser(c)
	}

	// Deferred loading is an alternative to splitting a catalog across agents.
	// In graph mode each agent node already carries its own slice of the catalog,
	// so the two are not combined; validation rejects that combination.
	if p.Spec.Tools.ToolSearch && p.Spec.Mode != manifest.ModeGraph {
		base := multiToolset{name: "adks_catalog", toolsets: toolsets}
		if len(tools) > 0 {
			base.toolsets = append(base.toolsets, staticToolset{name: "adks_tools", tools: tools})
		}
		search, err := toolsearch.New(base, toolsearch.Config{})
		if err != nil {
			return nil, fmt.Errorf("kit: deferred tool loading: %w", err)
		}
		toolsets = []tool.Toolset{search}
		tools = nil
	}

	sink, err := NewAuditSink(opts.AuditPath, opts.AuditVerbose)
	if err != nil {
		return nil, err
	}

	var a agent.Agent
	if p.Spec.Mode == manifest.ModeGraph {
		gb := &graphBuilder{
			ctx:      ctx,
			p:        p,
			opts:     opts,
			sink:     sink,
			tools:    tools,
			toolsets: toolsets,
			root:     llm,
			models:   map[string]model.LLM{},
		}
		a, err = gb.build(p.Spec.Graph)
		if err != nil {
			return nil, err
		}
	} else {
		cfg := llmagent.Config{
			Name:                  p.Spec.Agent.Name,
			Description:           p.Spec.Agent.Description,
			Instruction:           p.Spec.Agent.Instruction,
			Model:                 llm,
			GenerateContentConfig: generationConfig(p.Spec.Agent.Generation),
			Tools:                 tools,
			Toolsets:              toolsets,
			BeforeModelCallbacks:  NewLimitCallbacks(p.Spec.Policy.Limits),
			BeforeToolCallbacks:   []llmagent.BeforeToolCallback{NewLoopGuard()},
			AfterToolCallbacks:    NewAuditCallbacks(sink),
		}
		a, err = llmagent.New(cfg)
		if err != nil {
			return nil, fmt.Errorf("kit: build agent %q: %w", p.Spec.Agent.Name, err)
		}
	}

	sess, err := NewSessionService(ctx, p.Spec.Runtime.Session)
	if err != nil {
		return nil, err
	}
	if c, ok := sess.(io.Closer); ok {
		l.AddCloser(c)
	}
	arts, err := NewArtifactService(p.Spec.Runtime.Artifact)
	if err != nil {
		return nil, err
	}
	mem, err := NewMemoryService(p.Spec.Runtime.Memory)
	if err != nil {
		return nil, err
	}
	plugins, err := newPlugins(p)
	if err != nil {
		return nil, err
	}

	l.Agent = a
	l.Catalog = Catalog{Tools: tools, Toolsets: toolsets}
	l.Loader = agent.NewSingleLoader(a)
	l.Session = sess
	l.Artifact = arts
	l.Memory = mem
	l.Plugins = runnerPluginConfig(plugins)

	ok = true
	return l, nil
}
