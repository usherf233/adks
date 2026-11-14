package kit

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/mcptoolset"
	"google.golang.org/genai"

	"github.com/adks-dev/adks/manifest"
)

// MCPDeps is what an MCP connection needs from its host process.
type MCPDeps struct {
	// Headers are expanded at build time (${ENV}) and sent with every request.
	Headers map[string]string
	// Base is the shared round tripper, so MCP traffic reuses connections.
	Base http.RoundTripper
}

// MCPHandle separates the closable connection from the view handed to the agent.
//
// This distinction is not cosmetic. tool.FilterToolset returns a wrapper whose
// Close does nothing, so closing the filtered view would leak the stdio child
// process or the HTTP session. Only Raw is ever closed.
type MCPHandle struct {
	ID      string
	Raw     tool.Toolset
	Exposed tool.Toolset

	closer    func() error
	closeOnce sync.Once
	closeErr  error
}

// NewMCPHandle connects to one MCP server.
func NewMCPHandle(ref manifest.MCPRef, deps MCPDeps) (*MCPHandle, error) {
	// A caller that did not pre-resolve credentials still gets expanded headers:
	// the probe endpoint passes an empty MCPDeps, and without this it used to send
	// the literal "${TOKEN}" from the manifest.
	if deps.Headers == nil {
		headers, err := expandHeaders(ref.Headers)
		if err != nil {
			return nil, err
		}
		deps.Headers = headers
	}
	transport, err := buildTransport(ref, deps)
	if err != nil {
		return nil, err
	}
	raw, err := mcptoolset.New(mcptoolset.Config{Transport: transport})
	if err != nil {
		return nil, fmt.Errorf("mcp %q: connect: %w", ref.ID, err)
	}
	closer, ok := raw.(interface{ Close() error })
	if !ok {
		// Fail loudly: a silently unclosed connection leaks a process per build.
		return nil, fmt.Errorf("mcp %q: toolset of type %T does not expose Close", ref.ID, raw)
	}

	exposed := raw
	if len(ref.ToolAllow) > 0 {
		exposed = tool.FilterToolset(raw, tool.AllowedToolsPredicate(ref.ToolAllow))
	}
	return &MCPHandle{ID: ref.ID, Raw: raw, Exposed: exposed, closer: closer.Close}, nil
}

func buildTransport(ref manifest.MCPRef, deps MCPDeps) (mcp.Transport, error) {
	switch ref.Transport {
	case "stdio":
		if ref.Command == "" {
			return nil, fmt.Errorf("mcp %q: stdio transport needs a command", ref.ID)
		}
		cmd := exec.Command(ref.Command, ref.Args...)
		cmd.Env = filteredEnv(ref.EnvAllow)
		return &mcp.CommandTransport{Command: cmd}, nil

	case "http", "sse":
		host, err := mcpEndpointHost(ref)
		if err != nil {
			return nil, err
		}
		client := mcpHTTPClient(deps, host)
		if ref.Transport == "http" {
			return &mcp.StreamableClientTransport{Endpoint: ref.URL, HTTPClient: client}, nil
		}
		return &mcp.SSEClientTransport{Endpoint: ref.URL, HTTPClient: client}, nil
	}
	return nil, fmt.Errorf("mcp %q: unknown transport %q", ref.ID, ref.Transport)
}

// mcpEndpointHost validates the URL at runtime too: a manifest may reach Build
// without going through the editor's validator (the verifier and imports do).
func mcpEndpointHost(ref manifest.MCPRef) (string, error) {
	if err := manifest.ValidateEndpoint(ref.URL); err != nil {
		return "", fmt.Errorf("mcp %q: %w", ref.ID, err)
	}
	u, err := url.Parse(ref.URL)
	if err != nil {
		return "", fmt.Errorf("mcp %q: invalid url: %w", ref.ID, err)
	}
	return u.Host, nil
}

// mcpHTTPClient attaches the expanded headers, and only to the endpoint they
// were configured for. A redirect to another host must not receive them.
func mcpHTTPClient(deps MCPDeps, endpointHost string) *http.Client {
	if len(deps.Headers) == 0 && deps.Base == nil {
		return nil
	}
	rt := headerTransport{base: deps.Base, headers: deps.Headers, host: endpointHost}
	return &http.Client{
		Transport: rt,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("mcp: stopped after 5 redirects")
			}
			return nil
		},
	}
}

type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
	host    string
}

func (t headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if len(t.headers) > 0 && strings.EqualFold(req.URL.Host, t.host) {
		req = req.Clone(req.Context())
		for k, v := range t.headers {
			req.Header.Set(k, v)
		}
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

// baseEnv is always visible to a stdio server: without it a child process cannot
// resolve a command on Windows or find its own home directory.
var baseEnv = []string{"PATH", "HOME", "USERPROFILE", "SYSTEMROOT", "SystemRoot", "TEMP", "TMP", "LANG", "LC_ALL"}

// filteredEnv builds the child environment from the allow list only. A stdio MCP
// server is third-party code; handing it every secret in the parent environment
// is the easiest way to leak an API key.
func filteredEnv(allow []string) []string {
	want := map[string]bool{}
	for _, k := range baseEnv {
		want[strings.ToUpper(k)] = true
	}
	for _, k := range allow {
		want[strings.ToUpper(k)] = true
	}
	var out []string
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i > 0 {
			if want[strings.ToUpper(kv[:i])] {
				out = append(out, kv)
			}
		}
	}
	return out
}

// Close releases the connection. It only exists once and only closes Raw.
func (h *MCPHandle) Close() error {
	if h == nil {
		return nil
	}
	h.closeOnce.Do(func() {
		if h.closer != nil {
			h.closeErr = h.closer()
		}
		h.closer = nil
	})
	return h.closeErr
}

// MCPToolInfo is what a probe reports about one MCP tool.
type MCPToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Params      int    `json:"params"`
}

// ProbeMCP connects, lists the tools the agent would actually see, and closes.
// It is the only way to know an MCP server works before a conversation starts.
func ProbeMCP(ctx context.Context, ref manifest.MCPRef, deps MCPDeps) ([]MCPToolInfo, error) {
	h, err := NewMCPHandle(ref, deps)
	if err != nil {
		return nil, err
	}
	defer h.Close()

	tools, err := h.Exposed.Tools(probeContext{ctx})
	if err != nil {
		return nil, fmt.Errorf("mcp %q: list tools: %w", ref.ID, err)
	}
	out := make([]MCPToolInfo, 0, len(tools))
	for _, t := range tools {
		info := MCPToolInfo{Name: t.Name(), Description: t.Description()}
		if d, ok := t.(interface {
			Declaration() *genai.FunctionDeclaration
		}); ok {
			if decl := d.Declaration(); decl != nil && decl.Parameters != nil {
				info.Params = len(decl.Parameters.Properties)
			}
		}
		out = append(out, info)
	}
	return out, nil
}
