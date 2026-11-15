// Package manifest defines the adks AgentProject manifest: the single source of
// truth that a builder edits and that kit compiles into a running agent.
//
// The package is deliberately dependency-free apart from yaml.v3 so that it can
// be copied verbatim into an exported project (see internal/codegen).
package manifest

import "time"

const (
	// APIVersion is the only manifest schema this build understands.
	APIVersion = "adks/v1"
	// Kind is the only manifest kind this build understands.
	Kind = "AgentProject"
)

// Project is the root of an agent.yaml document.
type Project struct {
	APIVersion string   `yaml:"apiVersion" json:"apiVersion"`
	Kind       string   `yaml:"kind" json:"kind"`
	Metadata   Metadata `yaml:"metadata" json:"metadata"`
	Spec       Spec     `yaml:"spec" json:"spec"`
}

// Metadata identifies the project in the builder.
type Metadata struct {
	ID      string    `yaml:"id" json:"id"`
	Slug    string    `yaml:"slug" json:"slug"`
	Name    string    `yaml:"name" json:"name"`
	Created time.Time `yaml:"created" json:"created"`
	Updated time.Time `yaml:"updated" json:"updated"`
}

// Spec is the compiled part: everything that turns into ADK objects.
type Spec struct {
	// Mode selects what the compiler builds: ModeLLM (one LLM agent, the default)
	// or ModeGraph (a node/edge workflow). The empty value means ModeLLM, so a
	// manifest written before graphs existed keeps compiling unchanged.
	Mode    string      `yaml:"mode,omitempty" json:"mode,omitempty"`
	Agent   AgentSpec   `yaml:"agent" json:"agent"`
	Graph   *GraphSpec  `yaml:"graph,omitempty" json:"graph,omitempty"`
	Tools   ToolsSpec   `yaml:"tools,omitempty" json:"tools,omitempty"`
	Runtime RuntimeSpec `yaml:"runtime" json:"runtime"`
	Plugins []PluginRef `yaml:"plugins,omitempty" json:"plugins,omitempty"`
	Policy  PolicySpec  `yaml:"policy,omitempty" json:"policy,omitempty"`
}

// Compilation modes.
const (
	ModeLLM   = "llm"
	ModeGraph = "graph"
)

// Graph node kinds.
const (
	NodeAgent    = "agent"    // an LLM agent, the only kind that calls a model
	NodeTool     = "tool"     // one tool as a node, for a fixed pipeline step
	NodeRoute    = "route"    // deterministic branch: emits Routes, never calls a model
	NodeJoin     = "join"     // fan-in barrier: fires once every predecessor finished
	NodeSubGraph = "subgraph" // a nested graph, so a graph can be composed
)

// NodeRefStart is the sentinel a graph edge uses to name the entry point.
const NodeRefStart = "START"

// GraphSpec is a node/edge workflow; the "graph" mode of a project.
//
// It maps directly onto ADK's workflow package: NodeSpec becomes a
// workflow.Node, EdgeSpec becomes a workflow.Edge. Nothing here is interpreted
// at run time — the graph is a static structure assembled once at compile time,
// which is what lets a builder render and validate it without starting anything.
type GraphSpec struct {
	Nodes []NodeSpec `yaml:"nodes" json:"nodes"`
	Edges []EdgeSpec `yaml:"edges" json:"edges"`
}

// NodeSpec is one vertex of a graph. Which fields matter depends on Kind, and
// validation rejects the combinations that would otherwise be silently ignored.
type NodeSpec struct {
	ID          string `yaml:"id" json:"id"`
	Kind        string `yaml:"kind" json:"kind"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`

	// Kind == agent. Model nil inherits spec.agent.model; Tools empty means the
	// whole catalog, a non-empty list narrows it to those tool names.
	Instruction string     `yaml:"instruction,omitempty" json:"instruction,omitempty"`
	Model       *ModelSpec `yaml:"model,omitempty" json:"model,omitempty"`
	Tools       []string   `yaml:"tools,omitempty" json:"tools,omitempty"`
	Generation  *GenSpec   `yaml:"generation,omitempty" json:"generation,omitempty"`

	// Kind == tool.
	Tool string `yaml:"tool,omitempty" json:"tool,omitempty"`

	// Kind == route. Rules are evaluated in order against the node input; the
	// first match decides which outgoing route fires. Input names a state key to
	// read instead of the upstream output.
	Rules []RuleSpec `yaml:"rules,omitempty" json:"rules,omitempty"`
	Input string     `yaml:"input,omitempty" json:"input,omitempty"`

	// Kind == subgraph.
	Sub *GraphSpec `yaml:"sub,omitempty" json:"sub,omitempty"`

	// Any kind.
	TimeoutSec int        `yaml:"timeoutSec,omitempty" json:"timeoutSec,omitempty"`
	Retry      *RetrySpec `yaml:"retry,omitempty" json:"retry,omitempty"`
	Each       bool       `yaml:"each,omitempty" json:"each,omitempty"`
}

// RuleSpec is one branch condition of a route node.
type RuleSpec struct {
	Op    string `yaml:"op" json:"op"` // equals|contains|prefix|suffix|regex|always
	Match string `yaml:"match,omitempty" json:"match,omitempty"`
	Route string `yaml:"route" json:"route"`
}

// EdgeSpec is one directed connection. Route empty means the edge is
// unconditional; otherwise it only fires when the source node emitted that label.
type EdgeSpec struct {
	From  string `yaml:"from" json:"from"`
	To    string `yaml:"to" json:"to"`
	Route string `yaml:"route,omitempty" json:"route,omitempty"`
}

// RetrySpec is a node-level retry policy.
type RetrySpec struct {
	MaxAttempts int `yaml:"maxAttempts,omitempty" json:"maxAttempts,omitempty"`
	InitialSec  int `yaml:"initialSec,omitempty" json:"initialSec,omitempty"`
}

// AgentSpec configures the single LLM agent of this project.
type AgentSpec struct {
	Name        string    `yaml:"name" json:"name"`
	Description string    `yaml:"description" json:"description"`
	Instruction string    `yaml:"instruction" json:"instruction"`
	Model       ModelSpec `yaml:"model" json:"model"`
	Generation  *GenSpec  `yaml:"generation,omitempty" json:"generation,omitempty"`
}

// ModelSpec selects the LLM. Credentials never live here: only the name of the
// environment variable holding them.
type ModelSpec struct {
	Provider  string `yaml:"provider" json:"provider"` // gemini | openai | anthropic
	Name      string `yaml:"name" json:"name"`
	APIKeyEnv string `yaml:"apiKeyEnv,omitempty" json:"apiKeyEnv,omitempty"`
	BaseURL   string `yaml:"baseURL,omitempty" json:"baseURL,omitempty"`
}

// GenSpec maps to genai.GenerateContentConfig sampling knobs.
type GenSpec struct {
	Temperature     *float32 `yaml:"temperature,omitempty" json:"temperature,omitempty"`
	TopP            *float32 `yaml:"topP,omitempty" json:"topP,omitempty"`
	MaxOutputTokens *int32   `yaml:"maxOutputTokens,omitempty" json:"maxOutputTokens,omitempty"`
}

// ToolsSpec is the tool catalog. Registration order is not preserved: kit sorts
// by tool name so the prompt prefix stays byte-stable.
type ToolsSpec struct {
	Builtin    []string   `yaml:"builtin,omitempty" json:"builtin,omitempty"`
	HTTP       []HTTPSpec `yaml:"http,omitempty" json:"http,omitempty"`
	MCP        []MCPRef   `yaml:"mcp,omitempty" json:"mcp,omitempty"`
	ToolSearch bool       `yaml:"toolSearch,omitempty" json:"toolSearch,omitempty"`
}

// HTTPSpec declares a REST call as a tool. kit implements the call itself,
// because ADK Go ships no OpenAPI toolset.
type HTTPSpec struct {
	Name         string      `yaml:"name" json:"name"`
	Description  string      `yaml:"description" json:"description"`
	Method       string      `yaml:"method" json:"method"`
	URL          string      `yaml:"url" json:"url"`
	Params       []ParamSpec `yaml:"params,omitempty" json:"params,omitempty"`
	Auth         *AuthSpec   `yaml:"auth,omitempty" json:"auth,omitempty"`
	AllowedHosts []string    `yaml:"allowedHosts,omitempty" json:"allowedHosts,omitempty"`
	TimeoutSec   int         `yaml:"timeoutSec,omitempty" json:"timeoutSec,omitempty"`
	MaxRespBytes int         `yaml:"maxRespBytes,omitempty" json:"maxRespBytes,omitempty"`
	Destructive  bool        `yaml:"destructive,omitempty" json:"destructive,omitempty"`
}

// ParamSpec is one argument of an HTTP tool.
type ParamSpec struct {
	Name        string   `yaml:"name" json:"name"`
	Type        string   `yaml:"type" json:"type"` // string|integer|number|boolean|array_of_string
	In          string   `yaml:"in" json:"in"`     // path|query|body
	Required    bool     `yaml:"required,omitempty" json:"required,omitempty"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	Enum        []string `yaml:"enum,omitempty" json:"enum,omitempty"`
}

// AuthSpec describes how a secret is attached to an outgoing HTTP request.
type AuthSpec struct {
	Scheme    string `yaml:"scheme" json:"scheme"` // bearer|apikey-header|basic
	SecretEnv string `yaml:"secretEnv" json:"secretEnv"`
	Header    string `yaml:"header,omitempty" json:"header,omitempty"`
}

// MCPRef points at an MCP server.
type MCPRef struct {
	ID         string            `yaml:"id" json:"id"`
	Transport  string            `yaml:"transport" json:"transport"` // stdio|http|sse
	Command    string            `yaml:"command,omitempty" json:"command,omitempty"`
	Args       []string          `yaml:"args,omitempty" json:"args,omitempty"`
	EnvAllow   []string          `yaml:"envAllow,omitempty" json:"envAllow,omitempty"`
	URL        string            `yaml:"url,omitempty" json:"url,omitempty"`
	Headers    map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
	ToolAllow  []string          `yaml:"toolAllow,omitempty" json:"toolAllow,omitempty"`
	TimeoutSec int               `yaml:"timeoutSec,omitempty" json:"timeoutSec,omitempty"`
}

// RuntimeSpec configures where state lives and how the generated binary serves.
type RuntimeSpec struct {
	Server     ServerSpec      `yaml:"server" json:"server"`
	Session    SessionSpec     `yaml:"session" json:"session"`
	Memory     BackendSpec     `yaml:"memory" json:"memory"`
	Artifact   ArtifactSpec    `yaml:"artifact" json:"artifact"`
	Compaction *CompactionSpec `yaml:"compaction,omitempty" json:"compaction,omitempty"`
}

// ServerSpec maps to the launcher's web/api mode.
type ServerSpec struct {
	Mode       string `yaml:"mode" json:"mode"` // full|api|web|console
	Host       string `yaml:"host" json:"host"`
	Port       int    `yaml:"port" json:"port"`
	PathPrefix string `yaml:"pathPrefix,omitempty" json:"pathPrefix,omitempty"`
	A2A        bool   `yaml:"a2a,omitempty" json:"a2a,omitempty"`
	Auth       string `yaml:"auth,omitempty" json:"auth,omitempty"` // none|bearer
}

// SessionSpec selects the session backend.
type SessionSpec struct {
	Backend      string `yaml:"backend" json:"backend"` // memory|sqlite|postgres|mysql
	DSN          string `yaml:"dsn,omitempty" json:"dsn,omitempty"`
	AutoMigrate  bool   `yaml:"autoMigrate" json:"autoMigrate"`
	MaxOpenConns int    `yaml:"maxOpenConns,omitempty" json:"maxOpenConns,omitempty"`
}

// BackendSpec is a backend name only (memory has no knobs worth exposing).
type BackendSpec struct {
	Backend string `yaml:"backend" json:"backend"`
}

// ArtifactSpec selects the artifact backend.
type ArtifactSpec struct {
	Backend string `yaml:"backend" json:"backend"` // memory|local
	Dir     string `yaml:"dir,omitempty" json:"dir,omitempty"`
}

// CompactionSpec maps to runner.Config.Compaction.
type CompactionSpec struct {
	Enabled         bool   `yaml:"enabled" json:"enabled"`
	SummarizerModel string `yaml:"summarizerModel,omitempty" json:"summarizerModel,omitempty"`
}

// PluginRef enables one of the plugins kit knows how to build.
type PluginRef struct {
	Name   string            `yaml:"name" json:"name"`
	Kind   string            `yaml:"kind" json:"kind"` // builtin.logging|builtin.retryreflect|audit.webhook
	Config map[string]string `yaml:"config,omitempty" json:"config,omitempty"`
}

// PolicySpec holds the guardrails that are not per-tool.
type PolicySpec struct {
	Confirmation     string     `yaml:"confirmation,omitempty" json:"confirmation,omitempty"` // none|destructive|all
	DestructiveTools []string   `yaml:"destructiveTools,omitempty" json:"destructiveTools,omitempty"`
	Limits           LimitsSpec `yaml:"limits,omitempty" json:"limits,omitempty"`
}

// LimitsSpec bounds a single run.
type LimitsSpec struct {
	MaxLLMCalls        int `yaml:"maxLlmCalls,omitempty" json:"maxLlmCalls,omitempty"`
	MaxWallClockSec    int `yaml:"maxWallClockSec,omitempty" json:"maxWallClockSec,omitempty"`
	MaxToolConcurrency int `yaml:"maxToolConcurrency,omitempty" json:"maxToolConcurrency,omitempty"`
	// MaxTokensPerTurn stops a turn once the provider has billed more than this
	// many tokens (prompt + output + thoughts). Zero means unlimited. It is
	// enforced by a model callback, so the exported binary honours it too.
	MaxTokensPerTurn    int `yaml:"maxTokensPerTurn,omitempty" json:"maxTokensPerTurn,omitempty"`
	MaxInstructionBytes int `yaml:"maxInstructionBytes,omitempty" json:"maxInstructionBytes,omitempty"`
}
