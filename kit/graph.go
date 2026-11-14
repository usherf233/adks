package kit

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/workflow"

	"github.com/usherf233/adks/manifest"
)

// graphBuilder compiles spec.graph.
//
// A graph is a static structure: the whole thing is assembled here and handed to
// workflowagent.New, so nothing interprets the manifest at run time. The builder
// owns one model cache, because agent nodes frequently share a model and each
// newModel call builds a provider client.
type graphBuilder struct {
	ctx      context.Context
	p        *manifest.Project
	opts     Options
	sink     *AuditSink
	tools    []tool.Tool
	toolsets []tool.Toolset
	root     model.LLM
	models   map[string]model.LLM

	// subAgents collects every agent wrapped in a node. workflowagent needs them
	// registered, otherwise the runner cannot attribute an event to its author
	// and logs "Event from an unknown agent" on every turn.
	subAgents []agent.Agent
}

// build compiles a graph manifest into an agent. It is the graph-mode half of
// BuildWith; kept separate because it shares the model, tools and services steps
// but replaces the single-agent step entirely.
func (b *graphBuilder) build(g *manifest.GraphSpec) (agent.Agent, error) {
	edges, err := b.buildEdges(g)
	if err != nil {
		return nil, err
	}
	wk, err := workflowagent.New(workflowagent.Config{
		Name:        b.p.Spec.Agent.Name,
		Description: b.p.Spec.Agent.Description,
		Edges:       edges,
		SubAgents:   b.subAgents,
	})
	if err != nil {
		return nil, fmt.Errorf("kit: build workflow %q: %w", b.p.Spec.Agent.Name, err)
	}
	return wk, nil
}

func (b *graphBuilder) buildEdges(g *manifest.GraphSpec) ([]workflow.Edge, error) {
	if g == nil {
		return nil, fmt.Errorf("kit: graph is nil")
	}
	nodes := make(map[string]workflow.Node, len(g.Nodes))
	for i := range g.Nodes {
		spec := g.Nodes[i]
		n, err := b.buildNode(spec)
		if err != nil {
			return nil, fmt.Errorf("kit: node %q: %w", spec.ID, err)
		}
		nodes[spec.ID] = n
	}

	edges := make([]workflow.Edge, 0, len(g.Edges))
	for _, e := range g.Edges {
		to, ok := nodes[e.To]
		if !ok {
			return nil, fmt.Errorf("kit: edge to unknown node %q", e.To)
		}
		var from workflow.Node
		if e.From == manifest.NodeRefStart {
			from = workflow.Start
		} else {
			from, ok = nodes[e.From]
			if !ok {
				return nil, fmt.Errorf("kit: edge from unknown node %q", e.From)
			}
		}
		edge := workflow.Edge{From: from, To: to}
		if e.Route != "" {
			edge.Route = workflow.StringRoute(e.Route)
		}
		edges = append(edges, edge)
	}
	return edges, nil
}

func (b *graphBuilder) buildNode(spec manifest.NodeSpec) (workflow.Node, error) {
	cfg := nodeConfig(spec)
	switch spec.Kind {
	case manifest.NodeAgent:
		return b.agentNode(spec, cfg)
	case manifest.NodeTool:
		t, err := b.resolveTool(spec.Tool)
		if err != nil {
			return nil, err
		}
		return workflow.NewNamedToolNode(spec.ID, t, cfg)
	case manifest.NodeRoute:
		return newRouteNode(spec, cfg)
	case manifest.NodeJoin:
		return workflow.NewJoinNode(spec.ID), nil
	case manifest.NodeSubGraph:
		edges, err := b.buildEdges(spec.Sub)
		if err != nil {
			return nil, err
		}
		sub, err := workflow.NewWorkflowNode(spec.ID, edges)
		if err != nil {
			return nil, fmt.Errorf("subgraph: %w", err)
		}
		return sub, nil
	}
	return nil, fmt.Errorf("unknown node kind %q", spec.Kind)
}

func (b *graphBuilder) agentNode(spec manifest.NodeSpec, cfg workflow.NodeConfig) (workflow.Node, error) {
	llm, err := b.model(spec.Model)
	if err != nil {
		return nil, err
	}
	tools, toolsets := b.toolsFor(spec.Tools)
	a, err := llmagent.New(llmagent.Config{
		Name:                  spec.ID,
		Description:           spec.Description,
		Instruction:           spec.Instruction,
		Model:                 llm,
		GenerateContentConfig: generationConfig(spec.Generation),
		Tools:                 tools,
		Toolsets:              toolsets,
		// Every agent node shares the same session state, so the per-run budget
		// and the audit sink accumulate across the whole graph rather than
		// resetting per step.
		BeforeModelCallbacks: NewLimitCallbacks(b.p.Spec.Policy.Limits),
		BeforeToolCallbacks:  []llmagent.BeforeToolCallback{NewLoopGuard()},
		AfterToolCallbacks:   NewAuditCallbacks(b.sink),
	})
	if err != nil {
		return nil, fmt.Errorf("build agent: %w", err)
	}
	b.subAgents = append(b.subAgents, a)
	return workflow.NewAgentNode(a, cfg)
}

// toolsFor narrows the catalog to the names a node lists. An empty list means
// the whole catalog. Toolsets are kept whole: an MCP server exposes a set of
// tools whose names are only known after connecting, so filtering it by name here
// would silently empty it.
func (b *graphBuilder) toolsFor(names []string) ([]tool.Tool, []tool.Toolset) {
	if len(names) == 0 {
		return b.tools, b.toolsets
	}
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	var tools []tool.Tool
	for _, t := range b.tools {
		if want[t.Name()] {
			tools = append(tools, t)
		}
	}
	return tools, b.toolsets
}

// resolveTool finds one tool by name, including the ones living inside a
// toolset. Listing a toolset requires a context but no live session, which is
// what probeContext provides.
func (b *graphBuilder) resolveTool(name string) (tool.Tool, error) {
	for _, t := range b.tools {
		if t.Name() == name {
			return t, nil
		}
	}
	for _, ts := range b.toolsets {
		tools, err := ts.Tools(probeContext{b.ctx})
		if err != nil {
			return nil, fmt.Errorf("list toolset %q: %w", ts.Name(), err)
		}
		for _, t := range tools {
			if t.Name() == name {
				return t, nil
			}
		}
	}
	return nil, fmt.Errorf("tool %q not found in the catalog", name)
}

func (b *graphBuilder) model(spec *manifest.ModelSpec) (model.LLM, error) {
	if spec == nil {
		return b.root, nil
	}
	key := spec.Provider + "|" + spec.Name + "|" + spec.BaseURL
	if m, ok := b.models[key]; ok {
		return m, nil
	}
	m, err := newModel(b.ctx, *spec)
	if err != nil {
		if !b.opts.SkipModelCheck {
			return nil, err
		}
		m = &unavailableModel{reason: err.Error()}
	}
	b.models[key] = m
	return m, nil
}

// nodeConfig maps the per-node knobs onto ADK's NodeConfig.
func nodeConfig(spec manifest.NodeSpec) workflow.NodeConfig {
	cfg := workflow.NodeConfig{ParallelWorker: spec.Each}
	if spec.TimeoutSec > 0 {
		cfg.Timeout = time.Duration(spec.TimeoutSec) * time.Second
	}
	if r := spec.Retry; r != nil {
		rc := workflow.DefaultRetryConfig()
		rc.MaxAttempts = r.MaxAttempts
		rc.InitialDelay = time.Duration(r.InitialSec) * time.Second
		cfg.RetryConfig = rc
	}
	return cfg
}
