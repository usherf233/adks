package manifest

import (
	"strings"
	"testing"
)

// validProject returns a manifest that must pass with zero issues, so each test
// can mutate exactly one field.
func validProject(t *testing.T) *Project {
	t.Helper()
	p := New("support-bot", "Support Bot")
	p.Spec.Agent.Instruction = "You answer support questions using the tools you are given."
	ApplyDefaults(p)
	return p
}

func codepoints(n int) string { return strings.Repeat("字", n) }

func hasIssue(issues []Issue, level Level, pathContains, msgContains string) bool {
	for _, i := range issues {
		if i.Level != level || !strings.Contains(i.Path, pathContains) {
			continue
		}
		if msgContains == "" || strings.Contains(i.Message, msgContains) {
			return true
		}
	}
	return false
}

func TestValidateAcceptsStarterProject(t *testing.T) {
	p := validProject(t)
	if issues := Validate(p); len(issues) != 0 {
		t.Fatalf("starter project should be clean, got %v", issues)
	}
}

func TestValidateCatchesFieldErrors(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*Project)
		path     string
		contains string
	}{
		{
			name:     "slug with uppercase",
			mutate:   func(p *Project) { p.Metadata.Slug = "Support" },
			path:     "metadata.slug",
			contains: "must match",
		},
		{
			name:   "slug too short",
			mutate: func(p *Project) { p.Metadata.Slug = "a" },
			path:   "metadata.slug",
		},
		{
			name:     "agent named user",
			mutate:   func(p *Project) { p.Spec.Agent.Name = "user" },
			path:     "spec.agent.name",
			contains: "reserved",
		},
		{
			name:     "instruction exceeds byte budget",
			mutate:   func(p *Project) { p.Spec.Agent.Instruction = codepoints(20000) },
			path:     "spec.agent.instruction",
			contains: "exceeds",
		},
		{
			name:   "unknown provider",
			mutate: func(p *Project) { p.Spec.Agent.Model.Provider = "cohere" },
			path:   "spec.agent.model.provider",
		},
		{
			name:   "empty model name",
			mutate: func(p *Project) { p.Spec.Agent.Model.Name = "" },
			path:   "spec.agent.model.name",
		},
		{
			name:   "temperature out of range",
			mutate: func(p *Project) { f := float32(9); p.Spec.Agent.Generation = &GenSpec{Temperature: &f} },
			path:   "spec.agent.generation.temperature",
		},
		{
			name:     "unknown builtin tool",
			mutate:   func(p *Project) { p.Spec.Tools.Builtin = []string{"web_search"} },
			path:     "spec.tools.builtin[0]",
			contains: "unknown builtin",
		},
		{
			name: "duplicate tool name across kinds",
			mutate: func(p *Project) {
				p.Spec.Tools.Builtin = []string{"google_search"}
				p.Spec.Tools.HTTP = []HTTPSpec{{
					Name: "google_search", Description: "search the web for a query string",
					Method: "GET", URL: "https://api.example.com/s?q={query}",
					AllowedHosts: []string{"api.example.com"},
					Params:       []ParamSpec{{Name: "query", Type: "string", In: "query"}},
				}}
			},
			path:     "spec.tools.http[0].name",
			contains: "already used",
		},
		{
			name: "http description too short",
			mutate: func(p *Project) {
				p.Spec.Tools.HTTP = []HTTPSpec{{
					Name: "get_order", Description: "get order", Method: "GET",
					URL: "https://api.example.com/o", AllowedHosts: []string{"api.example.com"},
				}}
			},
			path:     "spec.tools.http[0].description",
			contains: "at least 15",
		},
		{
			name: "allowedHosts missing",
			mutate: func(p *Project) {
				p.Spec.Tools.HTTP = []HTTPSpec{{
					Name: "get_order", Description: "fetch one order by its identifier",
					Method: "GET", URL: "https://api.example.com/o",
				}}
			},
			path: "spec.tools.http[0].allowedHosts",
		},
		{
			name: "allowedHosts does not cover url host",
			mutate: func(p *Project) {
				p.Spec.Tools.HTTP = []HTTPSpec{{
					Name: "get_order", Description: "fetch one order by its identifier",
					Method: "GET", URL: "https://api.example.com/o",
					AllowedHosts: []string{"evil.example.com"},
				}}
			},
			path:     "spec.tools.http[0].allowedHosts",
			contains: "does not contain",
		},
		{
			name: "placeholder without param",
			mutate: func(p *Project) {
				p.Spec.Tools.HTTP = []HTTPSpec{{
					Name: "get_order", Description: "fetch one order by its identifier",
					Method: "GET", URL: "https://api.example.com/o/{order_id}",
					AllowedHosts: []string{"api.example.com"},
				}}
			},
			path:     "spec.tools.http[0].url",
			contains: "no matching param",
		},
		{
			name: "path param absent from url",
			mutate: func(p *Project) {
				p.Spec.Tools.HTTP = []HTTPSpec{{
					Name: "get_order", Description: "fetch one order by its identifier",
					Method: "GET", URL: "https://api.example.com/o",
					AllowedHosts: []string{"api.example.com"},
					Params:       []ParamSpec{{Name: "order_id", Type: "string", In: "path"}},
				}}
			},
			path:     "spec.tools.http[0].params[0].in",
			contains: "does not appear",
		},
		{
			name: "bad auth scheme",
			mutate: func(p *Project) {
				p.Spec.Tools.HTTP = []HTTPSpec{{
					Name: "get_order", Description: "fetch one order by its identifier",
					Method: "GET", URL: "https://api.example.com/o",
					AllowedHosts: []string{"api.example.com"},
					Auth:         &AuthSpec{Scheme: "oauth2", SecretEnv: "TOKEN"},
				}}
			},
			path: "spec.tools.http[0].auth.scheme",
		},
		{
			name:   "mcp stdio without command",
			mutate: func(p *Project) { p.Spec.Tools.MCP = []MCPRef{{ID: "files", Transport: "stdio"}} },
			path:   "spec.tools.mcp[0].command",
		},
		{
			name:   "mcp http without url",
			mutate: func(p *Project) { p.Spec.Tools.MCP = []MCPRef{{ID: "files", Transport: "http"}} },
			path:   "spec.tools.mcp[0].url",
		},
		{
			name:   "bad mcp transport",
			mutate: func(p *Project) { p.Spec.Tools.MCP = []MCPRef{{ID: "files", Transport: "grpc"}} },
			path:   "spec.tools.mcp[0].transport",
		},
		{
			name: "toolSearch combined with toolAllow",
			mutate: func(p *Project) {
				p.Spec.Tools.ToolSearch = true
				p.Spec.Tools.MCP = []MCPRef{{ID: "files", Transport: "stdio", Command: "mcp-files", ToolAllow: []string{"read_file"}}}
			},
			path:     "spec.tools.mcp[0].toolAllow",
			contains: "cannot be combined",
		},
		{
			name:   "sqlite without dsn",
			mutate: func(p *Project) { p.Spec.Runtime.Session = SessionSpec{Backend: "sqlite", AutoMigrate: true} },
			path:   "spec.runtime.session.dsn",
		},
		{
			name:   "unknown session backend",
			mutate: func(p *Project) { p.Spec.Runtime.Session.Backend = "redis" },
			path:   "spec.runtime.session.backend",
		},
		{
			name:   "unknown artifact backend",
			mutate: func(p *Project) { p.Spec.Runtime.Artifact.Backend = "s3" },
			path:   "spec.runtime.artifact.backend",
		},
		{
			name:   "bad server mode",
			mutate: func(p *Project) { p.Spec.Runtime.Server.Mode = "daemon" },
			path:   "spec.runtime.server.mode",
		},
		{
			name:   "port out of range",
			mutate: func(p *Project) { p.Spec.Runtime.Server.Port = 70000 },
			path:   "spec.runtime.server.port",
		},
		{
			name:   "bad confirmation mode",
			mutate: func(p *Project) { p.Spec.Policy.Confirmation = "sometimes" },
			path:   "spec.policy.confirmation",
		},
		{
			name:   "zero llm calls",
			mutate: func(p *Project) { p.Spec.Policy.Limits.MaxLLMCalls = 0 },
			path:   "spec.policy.limits.maxLlmCalls",
		},
		{
			name:   "unknown plugin kind",
			mutate: func(p *Project) { p.Spec.Plugins = []PluginRef{{Name: "audit", Kind: "builtin.telepathy"}} },
			path:   "spec.plugins[0].kind",
		},
		{
			name:   "wrong apiVersion",
			mutate: func(p *Project) { p.APIVersion = "adks/v2" },
			path:   "apiVersion",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := validProject(t)
			tc.mutate(p)
			issues := Validate(p)
			if !hasIssue(issues, LevelError, tc.path, tc.contains) {
				t.Fatalf("expected error at %q containing %q, got %v", tc.path, tc.contains, issues)
			}
		})
	}
}

func TestValidateWarnsOnPrefixInstability(t *testing.T) {
	p := validProject(t)
	p.Spec.Agent.Instruction += "\nToday is 2026-11-13, build 9f2c1e4a-0000-4000-8000-000000000000."
	issues := Validate(p)
	if !hasIssue(issues, LevelWarn, "spec.agent.instruction", "date literal") {
		t.Fatalf("expected date warning, got %v", issues)
	}
	if !hasIssue(issues, LevelWarn, "spec.agent.instruction", "UUID") {
		t.Fatalf("expected uuid warning, got %v", issues)
	}
}

func TestValidateWarnsOnManyTools(t *testing.T) {
	p := validProject(t)
	for i := 0; i < 31; i++ {
		p.Spec.Tools.HTTP = append(p.Spec.Tools.HTTP, HTTPSpec{
			Name:        "tool_" + string(rune('a'+i%26)) + string(rune('a'+i/26)),
			Description: "does a specific thing for the caller", Method: "GET",
			URL: "https://api.example.com/x", AllowedHosts: []string{"api.example.com"},
		})
	}
	issues := Validate(p)
	if !hasIssue(issues, LevelWarn, "spec.tools", "tool sources") {
		t.Fatalf("expected tool count warning, got %v", issues)
	}
}

func TestRoundTrip(t *testing.T) {
	p := validProject(t)
	p.Spec.Tools.HTTP = []HTTPSpec{{
		Name: "get_order", Description: "fetch one order by its identifier",
		Method: "GET", URL: "https://api.example.com/v1/orders/{order_id}",
		AllowedHosts: []string{"api.example.com"},
		Params:       []ParamSpec{{Name: "order_id", Type: "string", In: "path", Required: true}},
	}}
	raw, err := Encode(p)
	if err != nil {
		t.Fatal(err)
	}
	back, issues, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range issues {
		if i.Level == LevelError {
			t.Fatalf("round-tripped manifest has errors: %v", issues)
		}
	}
	if back.Metadata.Slug != p.Metadata.Slug || back.Spec.Tools.HTTP[0].Method != "GET" {
		t.Fatalf("round trip lost data: %+v", back)
	}
	if back.Spec.Agent.Model.APIKeyEnv != "GOOGLE_API_KEY" {
		t.Fatalf("defaults not applied: %q", back.Spec.Agent.Model.APIKeyEnv)
	}
}

func TestValidateEndpoint(t *testing.T) {
	for _, raw := range []string{
		"https://api.example.com/v1",
		"http://127.0.0.1:11434/v1", // local model proxies are a supported case
	} {
		if err := ValidateEndpoint(raw); err != nil {
			t.Errorf("endpoint %q should be accepted: %v", raw, err)
		}
	}
	for _, raw := range []string{
		"", "not-a-url", "ftp://api.example.com/x",
		"http://169.254.169.254/latest/meta-data/",
		"https://metadata.google.internal/computeMetadata/v1/",
		"https://user:pass@api.example.com/x",
	} {
		if err := ValidateEndpoint(raw); err == nil {
			t.Errorf("endpoint %q should be rejected", raw)
		}
	}
}

func TestDecodeRejectsMultipleDocuments(t *testing.T) {
	raw := []byte("apiVersion: adks/v1\nkind: AgentProject\nmetadata:\n  slug: demo-app\n---\nmetadata:\n  slug: hidden\n")
	if _, err := Decode(raw); err == nil || !strings.Contains(err.Error(), "multiple YAML documents") {
		t.Fatalf("a second YAML document must be rejected, got %v", err)
	}
}

func TestDecodeRejectsUnknownField(t *testing.T) {
	raw := []byte(`
apiVersion: adks/v1
kind: AgentProject
metadata:
  slug: demo-app
  name: Demo
spec:
  agent:
    name: demo_agent
    instruction: hi
    model:
      provider: gemini
  typoField: 1
`)
	if _, err := Decode(raw); err == nil {
		t.Fatal("expected unknown field to be rejected")
	}
}
