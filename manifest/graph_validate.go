package manifest

import (
	"fmt"
	"regexp"
	"strings"
)

// Graph validation runs entirely on the static structure, before anything is
// built. It exists because a workflow error otherwise surfaces deep inside the
// scheduler as a stalled run, which is the worst possible place to discover a
// missing edge.
//
// The checks fall into three groups: node shape (does each kind have what it
// needs), edge shape (are the labels producible), and topology (is the graph
// reachable, and does every route lead somewhere).

// validateGraph dispatches on spec.mode. The llm mode deliberately ignores a
// graph, but says so: a user who filled in a graph and forgot to switch the mode
// would otherwise see a green build that runs a different agent than they wrote.
func validateGraph(v *Validator, p *Project) {
	switch p.Spec.Mode {
	case ModeLLM:
		if g := p.Spec.Graph; g != nil && (len(g.Nodes) > 0 || len(g.Edges) > 0) {
			v.warnf("spec.graph", "is not compiled because mode is %q; set spec.mode: %s", ModeLLM, ModeGraph)
		}
		return
	case ModeGraph:
	default:
		v.errf("spec.mode", "must be one of %s|%s, got %q", ModeLLM, ModeGraph, p.Spec.Mode)
		return
	}
	if p.Spec.Graph == nil {
		v.errf("spec.graph", "is required when mode is %s", ModeGraph)
		return
	}
	if p.Spec.Tools.ToolSearch {
		// Deferred loading collapses the catalog into one search toolset, while a
		// graph narrows the catalog per agent node. Both mechanisms answer "too
		// many tools"; running them together would silently ignore the narrowing.
		v.errf("spec.tools.toolSearch", "cannot be combined with mode %s; narrow each agent node with its own tools list instead", ModeGraph)
	}
	validateGraphAt(v, p, p.Spec.Graph, "spec.graph", p.Spec.Agent.Name)
}

func validateGraphAt(v *Validator, p *Project, g *GraphSpec, path, rootName string) {
	if len(g.Nodes) == 0 {
		v.errf(path+".nodes", "at least one node is required")
		return
	}

	ids := make(map[string]int, len(g.Nodes))
	for i := range g.Nodes {
		n := &g.Nodes[i]
		np := fmt.Sprintf("%s.nodes[%d]", path, i)
		if strings.TrimSpace(n.ID) == "" {
			v.errf(np+".id", "is required")
			continue
		}
		if !reAgentName.MatchString(n.ID) {
			v.errf(np+".id", "must match %s", reAgentName.String())
		}
		if n.ID == NodeRefStart {
			v.errf(np+".id", "%q is reserved for the entry point", NodeRefStart)
		}
		if rootName != "" && n.ID == rootName {
			// The workflow is registered as an agent too, and an agent node's id
			// is that agent's name. Two agents with one name means the runner
			// cannot attribute events to either of them.
			v.errf(np+".id", "collides with spec.agent.name %q, which names the workflow itself", rootName)
		}
		if prev, ok := ids[n.ID]; ok {
			v.errf(np+".id", "duplicate node id %q (first used at nodes[%d])", n.ID, prev)
		} else {
			ids[n.ID] = i
		}
		validateNode(v, p, n, np, rootName)
	}

	validateEdges(v, g, path, ids)
	validateGraphTopology(v, g, path, ids)
	validateRouteBranches(v, g, path, ids)
}

// validateNode checks the per-kind fields and flags fields that the chosen kind
// ignores, because a silently ignored instruction reads as a working config.
func validateNode(v *Validator, p *Project, n *NodeSpec, path, rootName string) {
	switch n.Kind {
	case NodeAgent, NodeTool, NodeRoute, NodeJoin, NodeSubGraph:
	default:
		v.errf(path+".kind", "must be one of %s|%s|%s|%s|%s, got %q",
			NodeAgent, NodeTool, NodeRoute, NodeJoin, NodeSubGraph, n.Kind)
		return
	}

	if n.TimeoutSec < 0 {
		v.errf(path+".timeoutSec", "must not be negative")
	}
	if n.Each && n.Kind == NodeJoin {
		v.warnf(path+".each", "is ignored on a join node: a join has no input list to fan out over")
	}

	switch n.Kind {
	case NodeAgent:
		if strings.TrimSpace(n.Instruction) == "" {
			v.errf(path+".instruction", "is required for an agent node; it is the only thing that tells this step what to do")
		} else if lim := p.Spec.Policy.Limits.MaxInstructionBytes; lim > 0 && len(n.Instruction) > lim {
			v.errf(path+".instruction", "is %d bytes, exceeds limits.maxInstructionBytes=%d", len(n.Instruction), lim)
		}
		if reISODate.MatchString(n.Instruction) {
			v.warnf(path+".instruction", "contains a date literal; the prompt prefix will change daily and lose provider cache hits")
		}
		if m := n.Model; m != nil {
			switch m.Provider {
			case ProviderGemini, ProviderOpenAI, ProviderAnthropic:
			default:
				v.errf(path+".model.provider", "must be one of gemini|openai|anthropic, got %q", m.Provider)
			}
			if strings.TrimSpace(m.Name) == "" {
				v.errf(path+".model.name", "is required when a node overrides the model")
			}
		}
		for _, tn := range n.Tools {
			if !graphKnownTool(p, tn) {
				v.warnf(path+".tools", "%q is not defined in spec.tools; if it is an MCP tool it will resolve at run time", tn)
			}
		}

	case NodeTool:
		if strings.TrimSpace(n.Tool) == "" {
			v.errf(path+".tool", "is required for a tool node")
		} else if builtinTools[n.Tool] {
			// A tool node has to run the tool itself and forward its result. The
			// builtin google_search and url_context are model-side tools: they are
			// executed by the provider during generation, so there is no call to
			// make and no result to forward.
			v.errf(path+".tool", "%q is a model-side builtin tool and cannot be a node; use an http tool or an MCP tool", n.Tool)
		} else if !graphKnownTool(p, n.Tool) {
			if len(p.Spec.Tools.MCP) > 0 {
				v.warnf(path+".tool", "%q is not a declared http tool; it must exist on an MCP server and be runnable at run time", n.Tool)
			} else {
				v.errf(path+".tool", "%q is not defined in spec.tools.http and no MCP server is configured", n.Tool)
			}
		}

	case NodeRoute:
		if len(n.Rules) == 0 {
			v.errf(path+".rules", "at least one rule is required; a route node with no rules has no branch to emit")
		}
		seen := map[string]bool{}
		for j, r := range n.Rules {
			rp := fmt.Sprintf("%s.rules[%d]", path, j)
			switch r.Op {
			case "equals", "contains", "prefix", "suffix", "regex", "always":
			default:
				v.errf(rp+".op", "must be one of equals|contains|prefix|suffix|regex|always, got %q", r.Op)
			}
			if r.Op != "always" && strings.TrimSpace(r.Match) == "" {
				v.errf(rp+".match", "is required for op %q", r.Op)
			}
			if r.Op == "regex" && r.Match != "" {
				if _, err := regexp.Compile(r.Match); err != nil {
					v.errf(rp+".match", "is not a valid Go regexp: %v", err)
				}
			}
			if strings.TrimSpace(r.Route) == "" {
				v.errf(rp+".route", "is required; it is the label the matching outgoing edge must carry")
			} else if seen[r.Route] {
				v.errf(rp+".route", "label %q is produced by more than one rule; the first match wins, so the later one is unreachable", r.Route)
			}
			seen[r.Route] = true
		}

	case NodeJoin:
		if n.Instruction != "" || n.Tool != "" || len(n.Rules) > 0 || n.Sub != nil {
			v.warnf(path+".kind", "a join node only waits for its predecessors; the other node fields are ignored")
		}

	case NodeSubGraph:
		if n.Sub == nil {
			v.errf(path+".sub", "is required for a subgraph node")
		} else {
			validateGraphAt(v, p, n.Sub, path+".sub", rootName)
		}
	}
}

func validateEdges(v *Validator, g *GraphSpec, path string, ids map[string]int) {
	for i := range g.Edges {
		e := &g.Edges[i]
		ep := fmt.Sprintf("%s.edges[%d]", path, i)
		if strings.TrimSpace(e.From) == "" {
			v.errf(ep+".from", "is required")
		}
		if strings.TrimSpace(e.To) == "" {
			v.errf(ep+".to", "is required")
		}
		fromIdx, fromIsNode := ids[e.From]
		if e.From != NodeRefStart && !fromIsNode && e.From != "" {
			v.errf(ep+".from", "unknown node %q", e.From)
		}
		if _, ok := ids[e.To]; !ok && e.To != "" {
			v.errf(ep+".to", "unknown node %q (use node ids, not START, as the target)", e.To)
		}
		if e.From != "" && e.From == e.To {
			v.errf(ep, "self edge on node %q", e.From)
		}
		if e.Route == "" {
			continue
		}
		if e.From == NodeRefStart {
			v.errf(ep+".route", "START emits no labels; leave the route empty on edges out of START")
			continue
		}
		if !fromIsNode {
			continue
		}
		src := &g.Nodes[fromIdx]
		if src.Kind != NodeRoute {
			v.errf(ep+".route", "source %q is a %s node; only a route node emits labels, so this edge would never fire", e.From, src.Kind)
			continue
		}
		found := false
		for _, r := range src.Rules {
			if r.Route == e.Route {
				found = true
				break
			}
		}
		if !found {
			v.errf(ep+".route", "label %q is not produced by any rule of node %q", e.Route, e.From)
		}
	}
}

// validateRouteBranches closes the loop in the other direction: a rule whose
// label no edge uses is a branch that can never be taken, and a route node with
// no catch-all leaves the run stuck when nothing matches.
func validateRouteBranches(v *Validator, g *GraphSpec, path string, ids map[string]int) {
	labelsUsed := make(map[string]map[string]bool, len(g.Edges))
	unconditional := make(map[string]bool)
	for _, e := range g.Edges {
		if labelsUsed[e.From] == nil {
			labelsUsed[e.From] = map[string]bool{}
		}
		if e.Route == "" {
			unconditional[e.From] = true
		} else {
			labelsUsed[e.From][e.Route] = true
		}
	}

	for i := range g.Nodes {
		n := &g.Nodes[i]
		if n.Kind != NodeRoute {
			continue
		}
		np := fmt.Sprintf("%s.nodes[%d]", path, i)
		hasCatchAll := unconditional[n.ID]
		for j, r := range n.Rules {
			if !labelsUsed[n.ID][r.Route] {
				v.errf(fmt.Sprintf("%s.rules[%d].route", np, j),
					"no outgoing edge of %q carries label %q, so this branch is dead", n.ID, r.Route)
			}
			if r.Op == "always" {
				hasCatchAll = true
			}
		}
		if !hasCatchAll && len(n.Rules) > 0 {
			v.warnf(np+".rules",
				"no catch-all branch: when no rule matches, node %q emits no label and the run ends there. Add an {op: always} rule or an edge without a route", n.ID)
		}
	}
}

// validateGraphTopology reports unreachable nodes, nodes with no way in, and a
// graph whose START has no way out. Cycles are a warning rather than an error:
// ADK supports them and a retry loop is legitimate, but nothing bounds the number
// of turns except policy.limits.maxWallClockSec.
func validateGraphTopology(v *Validator, g *GraphSpec, path string, ids map[string]int) {
	out := map[string][]string{}
	indegree := map[string]int{}
	for _, e := range g.Edges {
		out[e.From] = append(out[e.From], e.To)
		if _, ok := ids[e.To]; ok {
			indegree[e.To]++
		}
	}

	if len(out[NodeRefStart]) == 0 {
		v.errf(path+".edges", "no edge leaves %s, so the graph can never start", NodeRefStart)
	}

	for i := range g.Nodes {
		n := &g.Nodes[i]
		np := fmt.Sprintf("%s.nodes[%d]", path, i)
		if indegree[n.ID] == 0 {
			v.errf(np+".id", "has no incoming edge; every node must be reachable from %s", NodeRefStart)
		}
	}

	reached := map[string]bool{NodeRefStart: true}
	queue := []string{NodeRefStart}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, next := range out[cur] {
			if !reached[next] {
				reached[next] = true
				queue = append(queue, next)
			}
		}
	}
	for i := range g.Nodes {
		if !reached[g.Nodes[i].ID] {
			v.warnf(fmt.Sprintf("%s.nodes[%d].id", path, i), "is not reachable from %s", NodeRefStart)
		}
	}

	if cycle := findCycle(g.Nodes, out); len(cycle) > 0 {
		v.warnf(path+".edges",
			"contains a cycle through %s; nothing bounds the number of turns except policy.limits.maxWallClockSec", strings.Join(cycle, " -> "))
	}
}

// findCycle returns one cycle, if the graph has any, for the error message.
func findCycle(nodes []NodeSpec, out map[string][]string) []string {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[string]int{}
	var stack []string

	var walk func(string) []string
	walk = func(id string) []string {
		color[id] = gray
		stack = append(stack, id)
		for _, next := range out[id] {
			switch color[next] {
			case gray:
				start := 0
				for i, s := range stack {
					if s == next {
						start = i
					}
				}
				return append(append([]string{}, stack[start:]...), next)
			case white:
				if c := walk(next); len(c) > 0 {
					return c
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[id] = black
		return nil
	}

	for _, n := range nodes {
		if color[n.ID] == white {
			if c := walk(n.ID); len(c) > 0 {
				return c
			}
		}
	}
	return nil
}

// graphKnownTool reports whether a name is resolvable from the manifest alone.
// MCP tool names are deliberately excluded: they only exist after connecting.
func graphKnownTool(p *Project, name string) bool {
	if builtinTools[name] {
		return true
	}
	for _, h := range p.Spec.Tools.HTTP {
		if h.Name == name {
			return true
		}
	}
	return false
}
