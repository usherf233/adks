package api

import (
	"net/http"

	"github.com/adks-dev/adks/manifest"
)

// graphView is the graph rendered for the UI: the same nodes and edges as the
// manifest, plus a depth computed from START so the front end can lay the graph
// out as layered columns instead of a hairball.
//
// It deliberately carries no compiled state. A graph is static, so this is a
// pure projection of the manifest and needs neither credentials nor a build.
type graphView struct {
	Nodes []graphNodeView `json:"nodes"`
	Edges []graphEdgeView `json:"edges"`
}

type graphNodeView struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Description string `json:"description,omitempty"`
	// Depth is the shortest number of edges from START, 1 being a direct
	// successor. Unreachable nodes get -1 so the UI can park them aside.
	Depth int `json:"depth"`
	// In/Out are the edge counts within this subgraph, for cheap sanity checking
	// in the UI without re-deriving the adjacency.
	In  int `json:"in"`
	Out int `json:"out"`
}

type graphEdgeView struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Route string `json:"route,omitempty"`
}

// graph serves the topology of the project's graph. For an llm-mode project it
// returns an empty view rather than an error: the absence of a graph is a
// fact about the project, not a failure to read it.
func (s *Server) graph(w http.ResponseWriter, r *http.Request) {
	slug := slugOf(r)
	p, _, err := s.store.Load(slug)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if p.Spec.Mode != manifest.ModeGraph || p.Spec.Graph == nil {
		writeJSON(w, http.StatusOK, graphView{})
		return
	}
	writeJSON(w, http.StatusOK, graphViewOf(p.Spec.Graph))
}

func graphViewOf(g *manifest.GraphSpec) graphView {
	if g == nil {
		return graphView{}
	}
	nodes := make([]graphNodeView, 0, len(g.Nodes))
	idx := make(map[string]int, len(g.Nodes))
	in := map[string]int{}
	out := map[string]int{}
	for i, n := range g.Nodes {
		idx[n.ID] = i
		nodes = append(nodes, graphNodeView{
			ID:          n.ID,
			Kind:        n.Kind,
			Description: nodeLabel(n),
			Depth:       -1,
		})
	}
	edges := make([]graphEdgeView, 0, len(g.Edges))
	adj := map[string][]string{}
	for _, e := range g.Edges {
		edges = append(edges, graphEdgeView{From: e.From, To: e.To, Route: e.Route})
		if e.From != manifest.NodeRefStart {
			out[e.From]++
		}
		in[e.To]++
		adj[e.From] = append(adj[e.From], e.To)
	}
	for i := range nodes {
		nodes[i].In = in[nodes[i].ID]
		nodes[i].Out = out[nodes[i].ID]
	}

	// Breadth-first from START. Nodes the graph cannot reach keep depth -1.
	reached := map[string]int{manifest.NodeRefStart: 0}
	queue := []string{manifest.NodeRefStart}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, next := range adj[cur] {
			if _, seen := reached[next]; !seen {
				reached[next] = reached[cur] + 1
				queue = append(queue, next)
			}
		}
	}
	for i := range nodes {
		if d, ok := reached[nodes[i].ID]; ok {
			nodes[i].Depth = d
		}
	}
	return graphView{Nodes: nodes, Edges: edges}
}

// nodeLabel picks the shortest useful label for a node in the topology view.
func nodeLabel(n manifest.NodeSpec) string {
	switch n.Kind {
	case manifest.NodeAgent:
		return n.Instruction
	case manifest.NodeTool:
		return n.Tool
	case manifest.NodeRoute:
		if len(n.Rules) == 0 {
			return "route"
		}
		return n.Rules[0].Route
	case manifest.NodeSubGraph:
		return "subgraph"
	}
	return n.Description
}
