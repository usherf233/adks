package kit

import (
	"io"
	"sort"

	"google.golang.org/adk/v2/tool"

	"github.com/adks-dev/adks/manifest"
)

// buildTools assembles the catalog. Both slices are returned because a toolset
// (MCP, deferred search) and a bare tool are different ADK shapes.
func buildTools(p *manifest.Project, opts Options) ([]tool.Tool, []tool.Toolset, []io.Closer, error) {
	var tools []tool.Tool
	var toolsets []tool.Toolset
	var closers []io.Closer

	for _, name := range p.Spec.Tools.Builtin {
		t, err := newBuiltinTool(name)
		if err != nil {
			return nil, nil, nil, err
		}
		tools = append(tools, t)
	}

	if len(p.Spec.Tools.HTTP) > 0 {
		semaphore := make(chan struct{}, maxConcurrency(p.Spec.Policy.Limits.MaxToolConcurrency))
		for _, spec := range p.Spec.Tools.HTTP {
			// One client per tool, with only that tool's allow list. Merging every
			// tool's hosts into one list lets a redirect from tool A reach tool B's
			// host carrying A's credentials; http.DefaultTransport still pools the
			// underlying connections across these clients.
			client := opts.HTTPClient
			if client == nil {
				client = NewHTTPClient(spec.AllowedHosts)
			}
			deps := HTTPToolDeps{
				Client:    client,
				Secrets:   opts.Secrets,
				Semaphore: semaphore,
			}
			if needsConfirmation(spec, p.Spec.Policy) {
				spec.Destructive = true
			}
			t, err := NewHTTPTool(spec, deps)
			if err != nil {
				return nil, nil, nil, err
			}
			tools = append(tools, t)
		}
	}

	for _, ref := range p.Spec.Tools.MCP {
		headers, err := expandHeaders(ref.Headers)
		if err != nil {
			return nil, nil, nil, err
		}
		h, err := NewMCPHandle(ref, MCPDeps{Headers: headers})
		if err != nil {
			return nil, nil, nil, err
		}
		// Returned as a closer so the caller registers it before anything else
		// can fail: an unregistered handle means a leaked child process.
		closers = append(closers, h)
		toolsets = append(toolsets, h.Exposed)
	}

	sortTools(tools)
	// Toolsets have no stable order of their own beyond manifest order, and that
	// order is preserved here on purpose: MCP servers are listed by id, which the
	// manifest already sorts by id for the user.
	sort.SliceStable(toolsets, func(i, j int) bool { return toolsets[i].Name() < toolsets[j].Name() })
	return tools, toolsets, closers, nil
}

func maxConcurrency(n int) int {
	if n <= 0 {
		return 4
	}
	return n
}

// needsConfirmation maps policy.confirmation onto a per-tool decision.
func needsConfirmation(spec manifest.HTTPSpec, pol manifest.PolicySpec) bool {
	for _, name := range pol.DestructiveTools {
		if name == spec.Name {
			return true
		}
	}
	switch pol.Confirmation {
	case "all":
		return true
	case "destructive":
		return spec.Destructive
	}
	return false
}
