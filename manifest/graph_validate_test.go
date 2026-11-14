package manifest

import (
	"strings"
	"testing"
)

// validGraphProject is a small but complete graph: a router, one agent branch,
// one tool branch and a shared summarizer. Mutating it in tests keeps every case
// one edit away from a valid manifest, so a reported error is attributable.
func validGraphProject() *Project {
	p := New("graph-demo", "Graph demo")
	p.Spec.Mode = ModeGraph
	p.Spec.Agent.Name = "ticket_workflow"
	p.Spec.Agent.Instruction = ""
	p.Spec.Tools.HTTP = []HTTPSpec{{
		Name: "get_ticket", Description: "fetch one support ticket by its id",
		Method: "GET", URL: "https://api.example.com/tickets/{id}",
		AllowedHosts: []string{"api.example.com"},
		Params:       []ParamSpec{{Name: "id", Type: "string", In: "path", Required: true}},
	}}
	p.Spec.Graph = &GraphSpec{
		Nodes: []NodeSpec{
			{ID: "classify", Kind: NodeRoute, Rules: []RuleSpec{
				{Op: "contains", Match: "refund", Route: "refund"},
				{Op: "always", Route: "query"},
			}},
			{ID: "refund_agent", Kind: NodeAgent, Instruction: "You handle refund requests."},
			{ID: "lookup", Kind: NodeTool, Tool: "get_ticket"},
			{ID: "summarize", Kind: NodeAgent, Instruction: "Summarize the previous answer."},
		},
		Edges: []EdgeSpec{
			{From: NodeRefStart, To: "classify"},
			{From: "classify", To: "refund_agent", Route: "refund"},
			{From: "classify", To: "lookup", Route: "query"},
			{From: "refund_agent", To: "summarize"},
			{From: "lookup", To: "summarize"},
		},
	}
	ApplyDefaults(p)
	return p
}

func findIssue(issues []Issue, path string) *Issue {
	for i := range issues {
		if issues[i].Path == path {
			return &issues[i]
		}
	}
	return nil
}

func TestValidateGraphAcceptsValidGraph(t *testing.T) {
	issues := Validate(validGraphProject())
	for _, i := range issues {
		if i.Level == LevelError {
			t.Fatalf("unexpected error: %v", issues)
		}
	}
}

func TestValidateGraphIssues(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*Project)
		wantPath  string
		wantLevel Level
	}{
		{"unknown mode", func(p *Project) { p.Spec.Mode = "pipeline" }, "spec.mode", LevelError},
		{"graph missing", func(p *Project) { p.Spec.Graph = nil }, "spec.graph", LevelError},
		{"duplicate node id", func(p *Project) {
			p.Spec.Graph.Nodes = append(p.Spec.Graph.Nodes, NodeSpec{ID: "summarize", Kind: NodeAgent, Instruction: "x"})
		}, "spec.graph.nodes[4].id", LevelError},
		{"START reserved", func(p *Project) { p.Spec.Graph.Nodes[0].ID = "START" }, "spec.graph.nodes[0].id", LevelError},
		{"node id collides with workflow name", func(p *Project) {
			p.Spec.Graph.Nodes[0].ID = p.Spec.Agent.Name
		}, "spec.graph.nodes[0].id", LevelError},
		{"bad node id", func(p *Project) { p.Spec.Graph.Nodes[0].ID = "Classify" }, "spec.graph.nodes[0].id", LevelError},
		{"unknown node kind", func(p *Project) { p.Spec.Graph.Nodes[1].Kind = "llm" }, "spec.graph.nodes[1].kind", LevelError},
		{"agent node needs instruction", func(p *Project) { p.Spec.Graph.Nodes[1].Instruction = "" }, "spec.graph.nodes[1].instruction", LevelError},
		{"tool node needs tool", func(p *Project) { p.Spec.Graph.Nodes[2].Tool = "" }, "spec.graph.nodes[2].tool", LevelError},
		{"tool node unknown tool", func(p *Project) { p.Spec.Graph.Nodes[2].Tool = "missing" }, "spec.graph.nodes[2].tool", LevelError},
		{"tool node cannot use a model-side builtin", func(p *Project) {
			p.Spec.Graph.Nodes[2].Tool = "google_search"
		}, "spec.graph.nodes[2].tool", LevelError},
		{"route node needs rules", func(p *Project) { p.Spec.Graph.Nodes[0].Rules = nil }, "spec.graph.nodes[0].rules", LevelError},
		{"unknown rule op", func(p *Project) { p.Spec.Graph.Nodes[0].Rules[0].Op = "fuzzy" }, "spec.graph.nodes[0].rules[0].op", LevelError},
		{"bad regex", func(p *Project) {
			p.Spec.Graph.Nodes[0].Rules[0].Op = "regex"
			p.Spec.Graph.Nodes[0].Rules[0].Match = "(["
		}, "spec.graph.nodes[0].rules[0].match", LevelError},
		{"match required", func(p *Project) { p.Spec.Graph.Nodes[0].Rules[0].Match = "" }, "spec.graph.nodes[0].rules[0].match", LevelError},
		{"duplicate rule label", func(p *Project) {
			p.Spec.Graph.Nodes[0].Rules[1].Route = "refund"
		}, "spec.graph.nodes[0].rules[1].route", LevelError},
		{"unknown edge target", func(p *Project) { p.Spec.Graph.Edges[0].To = "nowhere" }, "spec.graph.edges[0].to", LevelError},
		{"unknown edge source", func(p *Project) { p.Spec.Graph.Edges[0].From = "nowhere" }, "spec.graph.edges[0].from", LevelError},
		{"self edge", func(p *Project) {
			p.Spec.Graph.Edges = append(p.Spec.Graph.Edges, EdgeSpec{From: "summarize", To: "summarize"})
		}, "spec.graph.edges[5]", LevelError},
		{"label from non-route node", func(p *Project) {
			p.Spec.Graph.Edges[3].Route = "refund"
		}, "spec.graph.edges[3].route", LevelError},
		{"label not produced by rules", func(p *Project) {
			p.Spec.Graph.Edges[1].Route = "billing"
		}, "spec.graph.edges[1].route", LevelError},
		{"dead rule", func(p *Project) {
			p.Spec.Graph.Nodes[0].Rules = append(p.Spec.Graph.Nodes[0].Rules, RuleSpec{Op: "always", Route: "unused"})
		}, "spec.graph.nodes[0].rules[2].route", LevelError},
		{"no incoming edge", func(p *Project) {
			p.Spec.Graph.Edges = p.Spec.Graph.Edges[:1]
		}, "spec.graph.nodes[1].id", LevelError},
		{"START has no way out", func(p *Project) {
			p.Spec.Graph.Edges = p.Spec.Graph.Edges[1:]
		}, "spec.graph.edges", LevelError},
		{"unreachable cycle", func(p *Project) {
			p.Spec.Graph.Nodes = append(p.Spec.Graph.Nodes,
				NodeSpec{ID: "orphan_a", Kind: NodeAgent, Instruction: "a"},
				NodeSpec{ID: "orphan_b", Kind: NodeAgent, Instruction: "b"})
			p.Spec.Graph.Edges = append(p.Spec.Graph.Edges,
				EdgeSpec{From: "orphan_a", To: "orphan_b"},
				EdgeSpec{From: "orphan_b", To: "orphan_a"})
		}, "spec.graph.nodes[4].id", LevelWarn},
		{"toolSearch conflicts", func(p *Project) { p.Spec.Tools.ToolSearch = true }, "spec.tools.toolSearch", LevelError},
		{"join ignores fields", func(p *Project) {
			p.Spec.Graph.Nodes[2] = NodeSpec{ID: "lookup", Kind: NodeJoin, Tool: "get_ticket"}
		}, "spec.graph.nodes[2].kind", LevelWarn},
		{"subgraph requires sub", func(p *Project) {
			p.Spec.Graph.Nodes[2] = NodeSpec{ID: "lookup", Kind: NodeSubGraph}
		}, "spec.graph.nodes[2].sub", LevelError},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := validGraphProject()
			tc.mutate(p)
			ApplyDefaults(p)
			issues := Validate(p)
			got := findIssue(issues, tc.wantPath)
			if got == nil {
				t.Fatalf("expected an issue at %q, got: %v", tc.wantPath, issues)
			}
			if got.Level != tc.wantLevel {
				t.Fatalf("at %q: got level %s (%s), want %s", tc.wantPath, got.Level, got.Message, tc.wantLevel)
			}
		})
	}
}

// TestValidateGraphWarnsWithoutCatchAll is separate because the assertion is on
// the rule list as a whole, not one element.
func TestValidateGraphWarnsWithoutCatchAll(t *testing.T) {
	p := validGraphProject()
	p.Spec.Graph.Nodes[0].Rules = p.Spec.Graph.Nodes[0].Rules[:1]
	ApplyDefaults(p)
	issues := Validate(p)
	got := findIssue(issues, "spec.graph.nodes[0].rules")
	if got == nil || got.Level != LevelWarn {
		t.Fatalf("expected a catch-all warning, got %v", issues)
	}
	if !strings.Contains(got.Message, "catch-all") {
		t.Fatalf("unexpected message: %s", got.Message)
	}
}

func TestValidateGraphLLMModeWarnsAboutIgnoredGraph(t *testing.T) {
	p := validGraphProject()
	p.Spec.Mode = ModeLLM
	p.Spec.Agent.Instruction = "You are helpful."
	ApplyDefaults(p)
	got := findIssue(Validate(p), "spec.graph")
	if got == nil || got.Level != LevelWarn {
		t.Fatalf("expected a warning that the graph is ignored in llm mode")
	}
}

func TestValidateGraphNestedSubgraph(t *testing.T) {
	p := validGraphProject()
	p.Spec.Graph.Nodes[2] = NodeSpec{
		ID: "lookup", Kind: NodeSubGraph,
		Sub: &GraphSpec{
			Nodes: []NodeSpec{{ID: "inner", Kind: NodeTool, Tool: "get_ticket"}},
			Edges: []EdgeSpec{{From: NodeRefStart, To: "inner"}},
		},
	}
	ApplyDefaults(p)
	for _, i := range Validate(p) {
		if i.Level == LevelError {
			t.Fatalf("nested graph should validate: %v", i)
		}
	}
}

func TestGraphRoundTrip(t *testing.T) {
	p := validGraphProject()
	raw, err := Encode(p)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := Decode(raw)
	if err != nil {
		t.Fatalf("decode: %v\n%s", err, raw)
	}
	if got.Spec.Mode != ModeGraph {
		t.Fatalf("mode lost: %q", got.Spec.Mode)
	}
	if got.Spec.Graph == nil || len(got.Spec.Graph.Nodes) != 4 {
		t.Fatalf("graph lost: %+v", got.Spec.Graph)
	}
	if got.Spec.Graph.Edges[1].Route != "refund" {
		t.Fatalf("edge route lost: %+v", got.Spec.Graph.Edges[1])
	}
	for _, i := range Validate(got) {
		if i.Level == LevelError {
			t.Fatalf("round-tripped manifest invalid: %v", i)
		}
	}
}

// TestApplyDefaultsGraph checks the defaults that matter for compilation: a node
// model inherits the root provider instead of defaulting to gemini, and a retry
// policy gets usable numbers.
func TestApplyDefaultsGraph(t *testing.T) {
	p := New("g", "G")
	p.Spec.Mode = ModeGraph
	p.Spec.Agent.Model.Provider = ProviderOpenAI
	p.Spec.Graph = &GraphSpec{
		Nodes: []NodeSpec{
			{ID: "a", Kind: NodeAgent, Instruction: "x", Model: &ModelSpec{Name: "gpt-4o-mini"}, Retry: &RetrySpec{}},
			{ID: "r", Kind: NodeRoute, Rules: []RuleSpec{{Match: "x", Route: "go"}}},
		},
		Edges: []EdgeSpec{{From: NodeRefStart, To: "a"}, {From: "a", To: "r"}},
	}
	ApplyDefaults(p)

	m := p.Spec.Graph.Nodes[0].Model
	if m.Provider != ProviderOpenAI {
		t.Fatalf("node model should inherit openai, got %q", m.Provider)
	}
	if m.APIKeyEnv != "OPENAI_API_KEY" {
		t.Fatalf("node model should inherit the openai credential variable, got %q", m.APIKeyEnv)
	}
	if p.Spec.Graph.Nodes[0].Retry.MaxAttempts != 3 {
		t.Fatalf("retry default: %+v", p.Spec.Graph.Nodes[0].Retry)
	}
	if p.Spec.Graph.Nodes[1].Rules[0].Op != "contains" {
		t.Fatalf("rule op default: %q", p.Spec.Graph.Nodes[1].Rules[0].Op)
	}
}
