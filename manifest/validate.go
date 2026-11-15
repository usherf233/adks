package manifest

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"
)

// Level classifies an Issue.
type Level string

const (
	LevelError Level = "error"
	LevelWarn  Level = "warn"
)

// Issue is one validation finding, addressed by a dotted manifest path.
type Issue struct {
	Level   Level  `json:"level"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

func (i Issue) String() string {
	return fmt.Sprintf("%s: %s: %s", i.Level, i.Path, i.Message)
}

// Validator accumulates issues so that one pass reports every problem, not the
// first one.
type Validator struct {
	issues []Issue
}

func (v *Validator) errf(path, format string, a ...any) {
	v.issues = append(v.issues, Issue{Level: LevelError, Path: path, Message: fmt.Sprintf(format, a...)})
}

func (v *Validator) warnf(path, format string, a ...any) {
	v.issues = append(v.issues, Issue{Level: LevelWarn, Path: path, Message: fmt.Sprintf(format, a...)})
}

// HasErrors reports whether any error-level issue was recorded.
func (v *Validator) HasErrors() bool {
	for _, i := range v.issues {
		if i.Level == LevelError {
			return true
		}
	}
	return false
}

// Issues returns the findings sorted by path for stable UI rendering.
func (v *Validator) Issues() []Issue {
	out := append([]Issue(nil), v.issues...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Level != out[j].Level {
			return out[i].Level == LevelError
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// Validate runs every check and returns all findings. Callers must pass a
// project that has gone through Decode (or ApplyDefaults): limits are checked
// as written, so a raw document with zero-valued limits reports false errors.
func Validate(p *Project) []Issue {
	v := &Validator{}
	if p == nil {
		v.errf("", "manifest is empty")
		return v.Issues()
	}
	if p.APIVersion != APIVersion {
		v.errf("apiVersion", "must be %q, got %q", APIVersion, p.APIVersion)
	}
	if p.Kind != Kind {
		v.errf("kind", "must be %q, got %q", Kind, p.Kind)
	}
	if !reSlug.MatchString(p.Metadata.Slug) {
		v.errf("metadata.slug", "must match %s", reSlug.String())
	}
	if strings.TrimSpace(p.Metadata.Name) == "" {
		v.errf("metadata.name", "is required")
	}
	if p.Metadata.ID != "" && !strings.HasPrefix(p.Metadata.ID, "prj_") {
		v.warnf("metadata.id", "conventionally starts with prj_")
	}

	validateAgent(v, p)
	validateRuntime(v, p)
	validateTools(v, p)
	validatePolicy(v, p)
	validatePlugins(v, p)
	validateGraph(v, p)
	return v.Issues()
}

func validateAgent(v *Validator, p *Project) {
	a := &p.Spec.Agent
	if !reAgentName.MatchString(a.Name) {
		v.errf("spec.agent.name", "must match %s", reAgentName.String())
	}
	if a.Name == "user" {
		v.errf("spec.agent.name", `"user" is reserved by ADK for the human author`)
	}
	if n := utf8.RuneCountInString(a.Description); n > 200 {
		v.errf("spec.agent.description", "must be at most 200 characters, got %d", n)
	}
	if strings.TrimSpace(a.Description) == "" {
		v.warnf("spec.agent.description", "empty description lowers delegation and UI quality")
	}
	lim := p.Spec.Policy.Limits.MaxInstructionBytes
	if lim <= 0 {
		lim = 32 * 1024
	}
	if strings.TrimSpace(a.Instruction) == "" {
		// A graph project has no root instruction of its own: every agent node
		// carries one. The root agent fields then only name the workflow and
		// supply the default model.
		if p.Spec.Mode != ModeGraph {
			v.errf("spec.agent.instruction", "is required")
		}
	} else if len(a.Instruction) > lim {
		v.errf("spec.agent.instruction", "is %d bytes, exceeds limits.maxInstructionBytes=%d", len(a.Instruction), lim)
	} else if p.Spec.Mode == ModeGraph {
		v.warnf("spec.agent.instruction", "is ignored in graph mode; put the instruction on each agent node")
	}
	if reISODate.MatchString(a.Instruction) {
		v.warnf("spec.agent.instruction", "contains a date literal; the prompt prefix will change daily and lose provider cache hits")
	}
	if reUUID.MatchString(a.Instruction) {
		v.warnf("spec.agent.instruction", "contains a UUID literal; a random id in the instruction rewrites the cached prefix on every build")
	}

	switch a.Model.Provider {
	case ProviderGemini, ProviderOpenAI, ProviderAnthropic:
	default:
		v.errf("spec.agent.model.provider", "must be one of gemini|openai|anthropic, got %q", a.Model.Provider)
	}
	if strings.TrimSpace(a.Model.Name) == "" {
		v.errf("spec.agent.model.name", "is required")
	}
	if a.Model.APIKeyEnv != "" && !reEnvName.MatchString(a.Model.APIKeyEnv) {
		v.errf("spec.agent.model.apiKeyEnv", "must be an environment variable name like GOOGLE_API_KEY")
	}
	if a.Model.BaseURL != "" {
		if err := ValidateEndpoint(a.Model.BaseURL); err != nil {
			v.errf("spec.agent.model.baseURL", "%v", err)
		}
		if a.Model.Provider != ProviderOpenAI {
			v.warnf("spec.agent.model.baseURL", "only the openai provider uses baseURL in v1; it is ignored otherwise")
		}
	}
	if g := a.Generation; g != nil {
		if g.Temperature != nil && (*g.Temperature < 0 || *g.Temperature > 2) {
			v.errf("spec.agent.generation.temperature", "must be within [0,2]")
		}
		if g.TopP != nil && (*g.TopP < 0 || *g.TopP > 1) {
			v.errf("spec.agent.generation.topP", "must be within [0,1]")
		}
		if g.MaxOutputTokens != nil && (*g.MaxOutputTokens < 1 || *g.MaxOutputTokens > 8192) {
			v.errf("spec.agent.generation.maxOutputTokens", "must be within [1,8192]")
		}
	}
}

func validateRuntime(v *Validator, p *Project) {
	rt := &p.Spec.Runtime
	switch rt.Server.Mode {
	case "full", "api", "web", "console":
	default:
		v.errf("spec.runtime.server.mode", "must be one of full|api|web|console, got %q", rt.Server.Mode)
	}
	if rt.Server.Port < 1 || rt.Server.Port > 65535 {
		v.errf("spec.runtime.server.port", "must be within [1,65535]")
	}
	switch rt.Server.Auth {
	case "", "none":
	case "bearer":
	default:
		v.errf("spec.runtime.server.auth", "must be one of none|bearer")
	}
	if host := rt.Server.Host; host != "" && host != "127.0.0.1" && host != "localhost" && host != "::1" {
		if rt.Server.Auth == "none" || rt.Server.Auth == "" {
			v.warnf("spec.runtime.server.host", "binding %s without auth exposes the agent to the network", host)
		}
	}
	if pp := rt.Server.PathPrefix; pp != "" {
		// The value goes straight into the api sublauncher's flag, so a malformed
		// prefix is not a cosmetic problem: the exported binary fails to start.
		if !strings.HasPrefix(pp, "/") || strings.HasSuffix(pp, "/") || strings.ContainsAny(pp, " \t") {
			v.errf("spec.runtime.server.pathPrefix", "must start with /, must not end with /, and must not contain spaces, got %q", pp)
		}
	}
	if rt.Server.A2A && rt.Server.Auth == "" {
		v.warnf("spec.runtime.server.a2a", "the A2A endpoint is unauthenticated; set auth: bearer before exposing this project")
	}

	switch rt.Session.Backend {
	case "memory":
	case "sqlite", "postgres", "mysql":
		if strings.TrimSpace(rt.Session.DSN) == "" {
			v.errf("spec.runtime.session.dsn", "is required for backend %q", rt.Session.Backend)
		}
		if rt.Session.Backend == "sqlite" && rt.Session.MaxOpenConns > 1 {
			v.warnf("spec.runtime.session.maxOpenConns", "sqlite serializes writes; more than one connection only adds lock contention")
		}
	default:
		v.errf("spec.runtime.session.backend", "must be one of memory|sqlite|postgres|mysql, got %q", rt.Session.Backend)
	}

	switch rt.Memory.Backend {
	case "", "memory":
	default:
		v.errf("spec.runtime.memory.backend", "v1 only ships the memory backend, got %q", rt.Memory.Backend)
	}

	switch rt.Artifact.Backend {
	case "memory":
	case "local":
		if strings.TrimSpace(rt.Artifact.Dir) == "" {
			v.errf("spec.runtime.artifact.dir", "is required for backend local")
		}
	default:
		v.errf("spec.runtime.artifact.backend", "must be one of memory|local, got %q", rt.Artifact.Backend)
	}

	if c := rt.Compaction; c != nil && c.Enabled {
		v.warnf("spec.runtime.compaction.enabled", "compaction rewrites the prompt prefix (cache miss) and costs one extra LLM call per summary")
	}
}

func validateTools(v *Validator, p *Project) {
	t := &p.Spec.Tools
	seen := map[string]string{}

	claim := func(name, path string) bool {
		if prev, ok := seen[name]; ok {
			v.errf(path, "tool name %q already used by %s", name, prev)
			return false
		}
		seen[name] = path
		return true
	}

	for i, b := range t.Builtin {
		path := fmt.Sprintf("spec.tools.builtin[%d]", i)
		if !builtinTools[b] {
			v.errf(path, "unknown builtin %q, expected one of %s", b, strings.Join(BuiltinToolNames(), "|"))
			continue
		}
		claim(b, path+".name")
	}

	for i := range t.HTTP {
		h := &t.HTTP[i]
		path := fmt.Sprintf("spec.tools.http[%d]", i)
		if !reToolName.MatchString(h.Name) {
			v.errf(path+".name", "must match %s", reToolName.String())
		} else {
			claim(h.Name, path+".name")
		}
		if n := utf8.RuneCountInString(h.Description); n < 15 {
			v.errf(path+".description", "must be at least 15 characters (got %d); the description is what the model reads when choosing a tool", n)
		}
		switch h.Method {
		case "GET", "POST", "PUT", "PATCH", "DELETE":
		default:
			v.errf(path+".method", "must be one of GET|POST|PUT|PATCH|DELETE, got %q", h.Method)
		}
		u, err := url.Parse(h.URL)
		if err != nil || u.Host == "" {
			v.errf(path+".url", "is not a valid absolute URL")
		} else {
			if u.Scheme != "https" && u.Scheme != "http" {
				v.errf(path+".url", "scheme must be http or https")
			}
			if u.Scheme == "http" {
				v.warnf(path+".url", "plain http sends the tool payload and credentials unencrypted")
			}
			if len(h.AllowedHosts) == 0 {
				v.errf(path+".allowedHosts", "is required; list the host the tool is allowed to reach")
			} else if !HostInAllowList(u.Host, h.AllowedHosts) {
				v.errf(path+".allowedHosts", "does not contain the url host %q", u.Host)
			}
		}

		placeholders := map[string]bool{}
		for _, m := range rePlaceholder.FindAllStringSubmatch(h.URL, -1) {
			placeholders[m[1]] = true
		}
		usedPlaceholder := map[string]bool{}
		for j, prm := range h.Params {
			pp := fmt.Sprintf("%s.params[%d]", path, j)
			if !reToolName.MatchString(prm.Name) {
				v.errf(pp+".name", "must match %s", reToolName.String())
			}
			switch prm.Type {
			case "string", "integer", "number", "boolean", "array_of_string":
			default:
				v.errf(pp+".type", "must be one of string|integer|number|boolean|array_of_string")
			}
			switch prm.In {
			case "path":
				if !placeholders[prm.Name] {
					v.errf(pp+".in", "declared as path parameter but {%s} does not appear in url", prm.Name)
				}
				usedPlaceholder[prm.Name] = true
				if !prm.Required {
					v.warnf(pp+".required", "path parameters are always required")
				}
			case "query":
			case "body":
				if h.Method == "GET" || h.Method == "DELETE" {
					v.warnf(pp+".in", "%s with a body parameter is unusual; most servers ignore it", h.Method)
				}
			case "":
				v.errf(pp+".in", "must be one of path|query|body")
			default:
				v.errf(pp+".in", "must be one of path|query|body, got %q", prm.In)
			}
		}
		for ph := range placeholders {
			if !usedPlaceholder[ph] {
				v.errf(path+".url", "placeholder {%s} has no matching param declaration", ph)
			}
		}

		if a := h.Auth; a != nil {
			switch a.Scheme {
			case "bearer", "apikey-header", "basic":
			default:
				v.errf(path+".auth.scheme", "must be one of bearer|apikey-header|basic")
			}
			if !reEnvName.MatchString(a.SecretEnv) {
				v.errf(path+".auth.secretEnv", "must be an environment variable name like ORDER_API_TOKEN")
			}
			if a.Scheme == "apikey-header" && strings.TrimSpace(a.Header) == "" {
				v.errf(path+".auth.header", "is required for apikey-header")
			}
			if a.Header != "" && !reHeaderName.MatchString(a.Header) {
				v.errf(path+".auth.header", "is not a valid HTTP header name")
			}
		}
		// DELETE is destructive by nature; a destructive GET is a misconfiguration.
		if h.Destructive && h.Method == "GET" {
			v.warnf(path+".destructive", "a GET tool marked destructive will ask for confirmation on a read")
		}
	}

	for i := range t.MCP {
		r := &t.MCP[i]
		path := fmt.Sprintf("spec.tools.mcp[%d]", i)
		if !reMCPID.MatchString(r.ID) {
			v.errf(path+".id", "must match %s", reMCPID.String())
		} else {
			claim(r.ID, path+".id")
		}
		switch r.Transport {
		case "stdio":
			if strings.TrimSpace(r.Command) == "" {
				v.errf(path+".command", "is required for stdio transport")
			}
			if r.URL != "" {
				v.warnf(path+".url", "is ignored for stdio transport")
			}
		case "http", "sse":
			if strings.TrimSpace(r.URL) == "" {
				v.errf(path+".url", "is required for %s transport", r.Transport)
			} else if err := ValidateEndpoint(r.URL); err != nil {
				v.errf(path+".url", "%v", err)
			}
			if r.Command != "" {
				v.warnf(path+".command", "is ignored for %s transport", r.Transport)
			}
		default:
			v.errf(path+".transport", "must be one of stdio|http|sse, got %q", r.Transport)
		}
	}

	if t.ToolSearch {
		for i := range t.MCP {
			if len(t.MCP[i].ToolAllow) > 0 {
				v.errf(fmt.Sprintf("spec.tools.mcp[%d].toolAllow", i),
					"cannot be combined with tools.toolSearch: deferred loading decides disclosure, so a per-server allow list would be silently ignored")
			}
		}
		if len(t.Builtin)+len(t.HTTP)+len(t.MCP) < 5 {
			v.warnf("spec.tools.toolSearch", "with fewer than 5 tools, deferred loading adds a search round-trip and saves nothing")
		}
	}

	total := len(t.Builtin) + len(t.HTTP) + len(t.MCP)
	if total > 30 && !t.ToolSearch {
		v.warnf("spec.tools", "%d tool sources is a lot of prompt surface; narrow with toolAllow or enable tools.toolSearch", total)
	}
}

func validatePolicy(v *Validator, p *Project) {
	pol := &p.Spec.Policy
	switch pol.Confirmation {
	case "", "none", "destructive", "all":
	default:
		v.errf("spec.policy.confirmation", "must be one of none|destructive|all, got %q", pol.Confirmation)
	}

	known := map[string]bool{}
	for _, b := range p.Spec.Tools.Builtin {
		known[b] = true
	}
	for _, h := range p.Spec.Tools.HTTP {
		known[h.Name] = true
	}
	for _, d := range pol.DestructiveTools {
		if !known[d] {
			v.warnf("spec.policy.destructiveTools", "%q is not a tool defined in this manifest", d)
		}
	}
	if pol.Confirmation == "all" {
		v.warnf("spec.policy.confirmation", "every tool call will wait for a human; MCP tools cannot be gated in v1 and will run unconfirmed")
	}

	lim := pol.Limits
	if lim.MaxLLMCalls < 1 {
		v.errf("spec.policy.limits.maxLlmCalls", "must be at least 1")
	}
	if lim.MaxWallClockSec < 1 {
		v.errf("spec.policy.limits.maxWallClockSec", "must be at least 1")
	}
	if lim.MaxToolConcurrency < 1 {
		v.errf("spec.policy.limits.maxToolConcurrency", "must be at least 1")
	}
}

func validatePlugins(v *Validator, p *Project) {
	seen := map[string]bool{}
	for i, pl := range p.Spec.Plugins {
		path := fmt.Sprintf("spec.plugins[%d]", i)
		if strings.TrimSpace(pl.Name) == "" {
			v.errf(path+".name", "is required")
		} else if seen[pl.Name] {
			v.errf(path+".name", "duplicate plugin name %q", pl.Name)
		}
		seen[pl.Name] = true
		switch pl.Kind {
		case "builtin.logging", "builtin.retryreflect", "audit.webhook":
		default:
			v.errf(path+".kind", "must be one of builtin.logging|builtin.retryreflect|audit.webhook, got %q", pl.Kind)
		}
		if pl.Kind == "audit.webhook" {
			if err := ValidateEndpoint(pl.Config["url"]); err != nil {
				v.errf(path+".config.url", "%v", err)
			}
		}
	}
}
