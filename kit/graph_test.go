package kit

import (
	"context"
	"iter"
	"regexp"
	"strings"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"

	"github.com/adks-dev/adks/manifest"
)

// stubModel answers every request with a fixed string. It exists so a graph can
// be driven end to end without credentials: the interesting question is whether
// the graph routes to the right node, not whether a provider replies well.
type stubModel struct{ reply string }

func (m stubModel) Name() string { return "stub" }

func (m stubModel) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{
			Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: m.reply}}},
		}, nil)
	}
}

// graphProject builds a manifest with one router and two branches. Each branch
// names a different model so the test can tell which one ran.
func graphProject() *manifest.Project {
	p := manifest.New("graph-build", "Graph build")
	p.Spec.Mode = manifest.ModeGraph
	p.Spec.Agent.Name = "ticket_workflow"
	p.Spec.Agent.Instruction = ""
	p.Spec.Graph = &manifest.GraphSpec{
		Nodes: []manifest.NodeSpec{
			{ID: "classify", Kind: manifest.NodeRoute, Rules: []manifest.RuleSpec{
				{Op: "contains", Match: "refund", Route: "refund"},
				{Op: "always", Route: "other"},
			}},
			{ID: "refund_agent", Kind: manifest.NodeAgent, Instruction: "You handle refunds.",
				Model: &manifest.ModelSpec{Provider: "openai", Name: "model-a"}},
			{ID: "lookup_agent", Kind: manifest.NodeAgent, Instruction: "You look things up.",
				Model: &manifest.ModelSpec{Provider: "openai", Name: "model-b"}},
			{ID: "join", Kind: manifest.NodeJoin},
		},
		Edges: []manifest.EdgeSpec{
			{From: manifest.NodeRefStart, To: "classify"},
			{From: "classify", To: "refund_agent", Route: "refund"},
			{From: "classify", To: "lookup_agent", Route: "other"},
			{From: "refund_agent", To: "join"},
			{From: "lookup_agent", To: "join"},
		},
	}
	manifest.ApplyDefaults(p)
	return p
}

func graphBuilderFor(t *testing.T, p *manifest.Project, models map[string]model.LLM) *graphBuilder {
	t.Helper()
	sink, err := NewAuditSink("", false)
	if err != nil {
		t.Fatalf("audit sink: %v", err)
	}
	b := &graphBuilder{
		ctx:    context.Background(),
		p:      p,
		opts:   Options{SkipModelCheck: true},
		sink:   sink,
		root:   stubModel{reply: "ROOT"},
		models: map[string]model.LLM{},
	}
	for k, v := range models {
		b.models[k] = v
	}
	return b
}

// TestGraphRoutesConditionally is the end-to-end proof that a manifest-built
// graph runs and branches on a rule, rather than merely compiling. The two
// branches answer with different strings, so which one ran is unambiguous.
func TestGraphRoutesConditionally(t *testing.T) {
	p := graphProject()
	b := graphBuilderFor(t, p, map[string]model.LLM{
		"openai|model-a|": stubModel{reply: "BRANCH_REFUND"},
		"openai|model-b|": stubModel{reply: "BRANCH_OTHER"},
	})

	a, err := b.build(p.Spec.Graph)
	if err != nil {
		t.Fatalf("build graph: %v", err)
	}
	if a.Name() != "ticket_workflow" {
		t.Fatalf("workflow name = %q", a.Name())
	}

	rnr, err := runner.New(runner.Config{
		AppName:           "graph_test",
		Agent:             a,
		SessionService:    session.InMemoryService(),
		AutoCreateSession: true,
	})
	if err != nil {
		t.Fatalf("runner: %v", err)
	}

	cases := []struct {
		msg  string
		want string
	}{
		{"I need a refund for order 42", "BRANCH_REFUND"},
		{"what is my order status", "BRANCH_OTHER"},
	}
	for _, tc := range cases {
		t.Run(tc.msg, func(t *testing.T) {
			user := &genai.Content{Role: "user", Parts: []*genai.Part{{Text: tc.msg}}}
			var text strings.Builder
			for ev, err := range rnr.Run(context.Background(), "u1", "s1", user, agent.RunConfig{}) {
				if err != nil {
					t.Fatalf("run: %v", err)
				}
				text.WriteString(eventText(ev))
			}
			if !strings.Contains(text.String(), tc.want) {
				t.Fatalf("message %q produced %q, want it to contain %q", tc.msg, text.String(), tc.want)
			}
			if strings.Contains(text.String(), tc.want+"X") {
				t.Fatalf("both branches ran: %q", text.String())
			}
		})
	}
}

// eventText reads the text a node surfaced, whether it rode on Content or on the
// generic workflow Output field.
func eventText(ev *session.Event) string {
	if ev == nil {
		return ""
	}
	if ev.Content != nil {
		return textOf(ev.Content)
	}
	return textOf(ev.Output)
}

func TestRouteNodeEmitsRoutes(t *testing.T) {
	rules := []manifest.RuleSpec{
		{Op: "equals", Match: "stop", Route: "halt"},
		{Op: "always", Route: "go"},
	}
	n, err := newRouteNode(manifest.NodeSpec{ID: "r", Kind: manifest.NodeRoute, Rules: rules}, nodeConfig(manifest.NodeSpec{}))
	if err != nil {
		t.Fatalf("newRouteNode: %v", err)
	}

	cases := []struct {
		in   string
		want string
	}{
		{"stop", "halt"},
		{"  stop  ", "halt"},
		{"Stop now", "go"}, // equals is exact
		{"anything else", "go"},
	}
	for _, tc := range cases {
		var got []string
		var out any
		for ev, err := range n.Run(newFakeCtx(), tc.in) {
			if err != nil {
				t.Fatalf("run(%q): %v", tc.in, err)
			}
			got = ev.Routes
			out = ev.Output
		}
		if len(got) != 1 || got[0] != tc.want {
			t.Fatalf("input %q routed to %v, want [%s]", tc.in, got, tc.want)
		}
		if out != tc.in {
			t.Fatalf("input %q: output was %v, want the input forwarded", tc.in, out)
		}
	}
}

func TestRouteNodeNoMatchLeavesRoutesEmpty(t *testing.T) {
	n, err := newRouteNode(manifest.NodeSpec{ID: "r", Kind: manifest.NodeRoute,
		Rules: []manifest.RuleSpec{{Op: "contains", Match: "refund", Route: "refund"}}}, nodeConfig(manifest.NodeSpec{}))
	if err != nil {
		t.Fatalf("newRouteNode: %v", err)
	}
	for ev, err := range n.Run(newFakeCtx(), "hello") {
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(ev.Routes) != 0 {
			t.Fatalf("expected no route, got %v", ev.Routes)
		}
	}
}

func TestRouteNodeOperators(t *testing.T) {
	cases := []struct {
		op, match, in string
		want          bool
	}{
		{"contains", "refund", "I need a REFUND", true},
		{"contains", "refund", "hello", false},
		{"prefix", "order", "ORDER 42 is late", true},
		{"prefix", "order", "my order 42", false},
		{"suffix", "thanks", "ok thanks", true},
		{"suffix", "thanks", "thanks a lot", false},
		{"regex", `^ord-\d+$`, "ord-42", true},
		{"regex", `^ord-\d+$`, "ord-42x", false},
		{"always", "", "anything", true},
	}
	for _, tc := range cases {
		r := compiledRule{op: tc.op, match: tc.match}
		if tc.op == "regex" {
			re, err := regexp.Compile(tc.match)
			if err != nil {
				t.Fatalf("compile %q: %v", tc.match, err)
			}
			r.re = re
		}
		if got := ruleMatches(r, tc.in); got != tc.want {
			t.Fatalf("rule{%s %q} on %q = %v, want %v", tc.op, tc.match, tc.in, got, tc.want)
		}
	}
}

func TestRouteNodeReadsStateKey(t *testing.T) {
	n, err := newRouteNode(manifest.NodeSpec{ID: "r", Kind: manifest.NodeRoute,
		Input: "intent",
		Rules: []manifest.RuleSpec{{Op: "equals", Match: "billing", Route: "billing"}}}, nodeConfig(manifest.NodeSpec{}))
	if err != nil {
		t.Fatalf("newRouteNode: %v", err)
	}
	ctx := newFakeCtx()
	ctx.state["intent"] = "billing"

	var routes []string
	for ev, err := range n.Run(ctx, "ignored input") {
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		routes = ev.Routes
	}
	if len(routes) != 1 || routes[0] != "billing" {
		t.Fatalf("state routing produced %v", routes)
	}
}

func TestTextOf(t *testing.T) {
	if got := textOf("plain"); got != "plain" {
		t.Fatalf("string: %q", got)
	}
	c := &genai.Content{Parts: []*genai.Part{{Text: "a"}, {Text: "b"}}}
	if got := textOf(c); got != "ab" {
		t.Fatalf("content: %q", got)
	}
	if got := textOf(nil); got != "" {
		t.Fatalf("nil: %q", got)
	}
	if got := textOf(map[string]any{"k": 1}); got != `{"k":1}` {
		t.Fatalf("map: %q", got)
	}
}

func TestToolsForNarrowsByName(t *testing.T) {
	p := graphProject()
	p.Spec.Tools.HTTP = []manifest.HTTPSpec{
		{Name: "get_ticket", Description: "fetch one support ticket by id", Method: "GET",
			URL: "https://api.example.com/t/{id}", AllowedHosts: []string{"api.example.com"},
			Params: []manifest.ParamSpec{{Name: "id", Type: "string", In: "path", Required: true}}},
		{Name: "send_reply", Description: "send a reply to a support ticket", Method: "POST",
			URL: "https://api.example.com/reply", AllowedHosts: []string{"api.example.com"}},
	}
	manifest.ApplyDefaults(p)

	b := graphBuilderFor(t, p, nil)
	tools, toolsets, closers, err := buildTools(p, b.opts)
	if err != nil {
		t.Fatalf("buildTools: %v", err)
	}
	for _, c := range closers {
		t.Cleanup(func() { _ = c.Close() })
	}
	b.tools, b.toolsets = tools, toolsets

	if got, _ := b.toolsFor(nil); len(got) != 2 {
		t.Fatalf("no filter should keep the whole catalog, got %d tools", len(got))
	}
	if got, _ := b.toolsFor([]string{"get_ticket"}); len(got) != 1 || got[0].Name() != "get_ticket" {
		t.Fatalf("narrowing failed: %v", got)
	}

	if _, err := b.resolveTool("send_reply"); err != nil {
		t.Fatalf("resolveTool: %v", err)
	}
	if _, err := b.resolveTool("absent"); err == nil {
		t.Fatal("resolveTool should reject an unknown name")
	}
}

// TestBuildGraphCompilesAllNodeKinds exercises every node kind, including a
// nested subgraph, so a change to one constructor cannot silently break the
// whole graph path.
func TestBuildGraphCompilesAllNodeKinds(t *testing.T) {
	p := manifest.New("all-kinds", "All kinds")
	p.Spec.Mode = manifest.ModeGraph
	p.Spec.Agent.Name = "kinds_workflow"
	p.Spec.Agent.Instruction = ""
	p.Spec.Tools.HTTP = []manifest.HTTPSpec{{
		Name: "ping_backend", Description: "check that the backend is reachable",
		Method: "GET", URL: "https://api.example.com/ping", AllowedHosts: []string{"api.example.com"},
	}}
	p.Spec.Graph = &manifest.GraphSpec{
		Nodes: []manifest.NodeSpec{
			{ID: "entry", Kind: manifest.NodeRoute, Rules: []manifest.RuleSpec{{Op: "always", Route: "next"}}},
			{ID: "worker", Kind: manifest.NodeAgent, Instruction: "Do the work."},
			{ID: "search", Kind: manifest.NodeTool, Tool: "ping_backend"},
			{ID: "gather", Kind: manifest.NodeJoin},
			{ID: "inner", Kind: manifest.NodeSubGraph, Sub: &manifest.GraphSpec{
				Nodes: []manifest.NodeSpec{{ID: "deep", Kind: manifest.NodeAgent, Instruction: "One more step."}},
				Edges: []manifest.EdgeSpec{{From: manifest.NodeRefStart, To: "deep"}},
			}},
			{ID: "final", Kind: manifest.NodeAgent, Instruction: "Wrap up."},
		},
		Edges: []manifest.EdgeSpec{
			{From: manifest.NodeRefStart, To: "entry"},
			{From: "entry", To: "worker", Route: "next"},
			{From: "entry", To: "search", Route: "next"},
			{From: "worker", To: "gather"},
			{From: "search", To: "gather"},
			{From: "gather", To: "inner"},
			{From: "inner", To: "final"},
		},
	}
	manifest.ApplyDefaults(p)
	if errs := manifest.Validate(p); hasErrors(errs) {
		t.Fatalf("test manifest invalid: %v", errs)
	}

	b := graphBuilderFor(t, p, nil)
	tools, toolsets, closers, err := buildTools(p, b.opts)
	if err != nil {
		t.Fatalf("buildTools: %v", err)
	}
	for _, c := range closers {
		t.Cleanup(func() { _ = c.Close() })
	}
	b.tools, b.toolsets = tools, toolsets

	a, err := b.build(p.Spec.Graph)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if a == nil {
		t.Fatal("nil agent")
	}
	// worker, deep and final are the LLM agents that must be registered with the
	// workflow so the runner can attribute their events.
	if len(b.subAgents) != 3 {
		t.Fatalf("expected 3 registered sub agents, got %d", len(b.subAgents))
	}
}

func hasErrors(issues []manifest.Issue) bool {
	for _, i := range issues {
		if i.Level == manifest.LevelError {
			return true
		}
	}
	return false
}

func TestBuildGraphRejectsUnknownNode(t *testing.T) {
	p := graphProject()
	p.Spec.Graph.Edges[0].To = "ghost"
	b := graphBuilderFor(t, p, nil)
	if _, err := b.build(p.Spec.Graph); err == nil {
		t.Fatal("expected an error for an unknown node")
	}
}

func TestBuildGraphViaBuildWith(t *testing.T) {
	p := graphProject()
	p.Spec.Runtime.Session = manifest.SessionSpec{Backend: "sqlite", DSN: "file::memory:?cache=shared"}
	layer, err := BuildWith(context.Background(), p, Options{SkipModelCheck: true})
	if err != nil {
		t.Fatalf("BuildWith graph: %v", err)
	}
	t.Cleanup(func() { _ = layer.Close(context.Background()) })
	if layer.Agent == nil || layer.Agent.Name() != "ticket_workflow" {
		t.Fatalf("compiled agent = %+v", layer.Agent)
	}
}
