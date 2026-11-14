package api

import (
	"strings"

	"github.com/usherf233/adks/kit"
	"github.com/usherf233/adks/manifest"
)

// estimate is a capacity hint, never a bill. The UI labels it as an estimate.
type estimate struct {
	InstructionTokens int `json:"instructionTokens"`
	ToolSchemaTokens  int `json:"toolSchemaTokens"`
	TotalTokens       int `json:"totalTokens"`
	Chars             int `json:"chars"`
	ToolCount         int `json:"toolCount"`
	// Graph shape, filled only for graph mode. A graph pays its prompt cost once
	// per agent node, so the node count is what turns a total into a per-step hint.
	NodeCount int `json:"nodeCount,omitempty"`
	EdgeCount int `json:"edgeCount,omitempty"`
}

// estimateFor sizes the prompt the model will actually receive: the instruction
// plus every tool declaration. Tool schemas are the part people forget, and a
// dozen MCP servers can dwarf a long instruction.
//
// In graph mode the prompt is paid per agent node, so the node instructions are
// summed and the graph shape is reported alongside. That sum is an upper bound:
// a node that names its own tools pays less than the whole catalog.
func estimateFor(p *manifest.Project) estimate {
	instruction := p.Spec.Agent.Instruction
	chars := len(instruction)
	nodeCount, edgeCount := 0, 0
	if p.Spec.Mode == manifest.ModeGraph && p.Spec.Graph != nil {
		walkGraph(p.Spec.Graph, func(n manifest.NodeSpec) {
			if n.Kind == manifest.NodeAgent && n.Instruction != "" {
				chars += len(n.Instruction)
				instruction += "\n" + n.Instruction
			}
		})
		nodeCount, edgeCount = graphSize(p.Spec.Graph)
	}
	e := estimate{
		Chars:     chars,
		ToolCount: len(p.Spec.Tools.Builtin) + len(p.Spec.Tools.HTTP) + len(p.Spec.Tools.MCP),
		NodeCount: nodeCount,
		EdgeCount: edgeCount,
	}
	e.InstructionTokens = kit.EstimateTokens(instruction)
	schemaBytes := 0
	for _, h := range p.Spec.Tools.HTTP {
		schemaBytes += len(h.Description) + 40
		for _, prm := range h.Params {
			schemaBytes += len(prm.Name) + len(prm.Description) + 24
		}
	}
	// Builtin tools and MCP servers are counted per server, not per tool: their
	// real schema is only known after connecting.
	schemaBytes += len(p.Spec.Tools.Builtin) * 40
	schemaBytes += len(p.Spec.Tools.MCP) * 600
	e.ToolSchemaTokens = kit.EstimateTokens(strings.Repeat("x", schemaBytes))
	e.TotalTokens = e.InstructionTokens + e.ToolSchemaTokens
	return e
}

// walkGraphs visits every graph, including nested subgraphs. Node and edge
// counts share this traversal so a new nesting level cannot update only one.
func walkGraphs(g *manifest.GraphSpec, fn func(*manifest.GraphSpec)) {
	if g == nil {
		return
	}
	fn(g)
	for _, n := range g.Nodes {
		if n.Kind == manifest.NodeSubGraph {
			walkGraphs(n.Sub, fn)
		}
	}
}

func walkGraph(g *manifest.GraphSpec, fn func(manifest.NodeSpec)) {
	walkGraphs(g, func(gr *manifest.GraphSpec) {
		for _, n := range gr.Nodes {
			fn(n)
		}
	})
}

// graphSize counts nodes and edges across nested subgraphs.
func graphSize(g *manifest.GraphSpec) (nodes, edges int) {
	walkGraphs(g, func(gr *manifest.GraphSpec) {
		nodes += len(gr.Nodes)
		edges += len(gr.Edges)
	})
	return nodes, edges
}
