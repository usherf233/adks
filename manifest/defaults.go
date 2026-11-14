package manifest

import (
	"strings"
	"time"
)

// Provider default models and credential variables.
const (
	ProviderGemini    = "gemini"
	ProviderOpenAI    = "openai"
	ProviderAnthropic = "anthropic"
)

// DefaultAPIKeyEnv returns the conventional credential variable for a provider.
func DefaultAPIKeyEnv(provider string) string {
	switch provider {
	case ProviderOpenAI:
		return "OPENAI_API_KEY"
	case ProviderAnthropic:
		return "ANTHROPIC_API_KEY"
	default:
		return "GOOGLE_API_KEY"
	}
}

// DefaultModelName is used when the manifest names a provider but no model.
func DefaultModelName(provider string) string {
	switch provider {
	case ProviderOpenAI:
		return "gpt-4o-mini"
	case ProviderAnthropic:
		return "claude-sonnet-4-5"
	default:
		return "gemini-flash-latest"
	}
}

// ApplyDefaults fills every optional field with the value documented in the
// design. It is idempotent and never overwrites a value the user set, except for
// fields whose zero value is meaningless (a port of 0, a positive count of 0).
func ApplyDefaults(p *Project) {
	if p.APIVersion == "" {
		p.APIVersion = APIVersion
	}
	if p.Kind == "" {
		p.Kind = Kind
	}
	if p.Spec.Mode == "" {
		p.Spec.Mode = ModeLLM
	}
	if p.Spec.Agent.Model.Provider == "" {
		p.Spec.Agent.Model.Provider = ProviderGemini
	}
	m := &p.Spec.Agent.Model
	if m.Name == "" {
		m.Name = DefaultModelName(m.Provider)
	}
	if m.APIKeyEnv == "" {
		m.APIKeyEnv = DefaultAPIKeyEnv(m.Provider)
	}
	applyGraphDefaults(p)

	for i := range p.Spec.Tools.HTTP {
		h := &p.Spec.Tools.HTTP[i]
		if h.Method == "" {
			h.Method = "GET"
		}
		h.Method = strings.ToUpper(h.Method)
		if h.TimeoutSec <= 0 {
			h.TimeoutSec = 20
		}
		if h.MaxRespBytes <= 0 {
			h.MaxRespBytes = 64 * 1024
		}
	}
	for i := range p.Spec.Tools.MCP {
		r := &p.Spec.Tools.MCP[i]
		if r.Transport == "" {
			r.Transport = "stdio"
		}
		if r.TimeoutSec <= 0 {
			r.TimeoutSec = 20
		}
	}

	rt := &p.Spec.Runtime
	if rt.Server.Mode == "" {
		rt.Server.Mode = "full"
	}
	if rt.Server.Host == "" {
		rt.Server.Host = "127.0.0.1"
	}
	if rt.Server.Port <= 0 {
		rt.Server.Port = 8080
	}
	if rt.Server.Auth == "" {
		rt.Server.Auth = "none"
	}
	if rt.Session.Backend == "" {
		rt.Session.Backend = "sqlite"
	}
	if rt.Session.DSN == "" {
		switch rt.Session.Backend {
		case "sqlite":
			rt.Session.DSN = "file:./.adks/sessions.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
		}
	}
	if rt.Session.MaxOpenConns <= 0 {
		if rt.Session.Backend == "sqlite" {
			rt.Session.MaxOpenConns = 1
		} else {
			rt.Session.MaxOpenConns = 10
		}
	}
	if rt.Session.Backend != "memory" {
		// Always on for SQL backends: ADK adds columns between versions and a
		// missing column fails at write time, not at start time. Turning it off
		// is not a choice v1 offers.
		rt.Session.AutoMigrate = true
	}
	if rt.Memory.Backend == "" {
		rt.Memory.Backend = "memory"
	}
	if rt.Artifact.Backend == "" {
		rt.Artifact.Backend = "local"
	}
	if rt.Artifact.Backend == "local" && rt.Artifact.Dir == "" {
		rt.Artifact.Dir = "./.adks/artifacts"
	}

	if p.Spec.Policy.Confirmation == "" {
		p.Spec.Policy.Confirmation = "destructive"
	}
	lim := &p.Spec.Policy.Limits
	if lim.MaxLLMCalls <= 0 {
		lim.MaxLLMCalls = 20
	}
	if lim.MaxWallClockSec <= 0 {
		lim.MaxWallClockSec = 120
	}
	if lim.MaxToolConcurrency <= 0 {
		lim.MaxToolConcurrency = 4
	}
	if lim.MaxInstructionBytes <= 0 {
		lim.MaxInstructionBytes = 32 * 1024
	}
}

// Touch stamps Updated and, when zero, Created.
func (p *Project) Touch(now time.Time) {
	if p.Metadata.Created.IsZero() {
		p.Metadata.Created = now
	}
	p.Metadata.Updated = now
}

// applyGraphDefaults walks a graph, including nested subgraphs, and fills the
// values whose zero is meaningless. Node ids and kinds are left alone: a missing
// id is an error, not something to invent.
//
// It runs after the root agent model is resolved, because a node model inherits
// the provider of the graph's default model.
func applyGraphDefaults(p *Project) {
	if p == nil || p.Spec.Graph == nil {
		return
	}
	applyGraphAt(p, p.Spec.Graph)
}

func applyGraphAt(p *Project, g *GraphSpec) {
	if g == nil {
		return
	}
	base := p.Spec.Agent.Model
	for i := range g.Nodes {
		n := &g.Nodes[i]
		if n.Kind == "" {
			n.Kind = NodeAgent
		}
		if n.Model != nil {
			if n.Model.Provider == "" {
				n.Model.Provider = base.Provider
			}
			if n.Model.Name == "" {
				n.Model.Name = DefaultModelName(n.Model.Provider)
			}
			if n.Model.APIKeyEnv == "" {
				n.Model.APIKeyEnv = DefaultAPIKeyEnv(n.Model.Provider)
			}
		}
		if n.Kind == NodeRoute {
			for j := range n.Rules {
				if n.Rules[j].Op == "" {
					n.Rules[j].Op = "contains"
				}
			}
		}
		if n.Kind == NodeSubGraph {
			applyGraphAt(p, n.Sub)
		}
		if n.Retry != nil {
			if n.Retry.MaxAttempts <= 0 {
				n.Retry.MaxAttempts = 3
			}
			if n.Retry.InitialSec <= 0 {
				n.Retry.InitialSec = 1
			}
		}
	}
}
