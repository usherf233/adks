package kit

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool/toolconfirmation"
	"google.golang.org/genai"
	"gopkg.in/yaml.v3"
)

// Regression cases are the third layer of verification: build and vet prove the
// project can run, these prove it does the right thing after an edit.
//
// The types and the verdicts live in kit rather than in the builder because the
// exported project carries evals.yaml and must be able to run it itself — with
// `agent -eval` in CI. Only the driver differs: the builder streams events to a
// browser and records cost, this one prints a table. The assertions are shared, so
// a case cannot pass in one place and fail in the other.

// Expect is what a case asserts. Every field is optional, but a case with no
// expectation at all is rejected: it would pass no matter what the agent did.
type Expect struct {
	ToolsUsed    []string `yaml:"toolsUsed,omitempty" json:"toolsUsed,omitempty"`
	ToolsNotUsed []string `yaml:"toolsNotUsed,omitempty" json:"toolsNotUsed,omitempty"`
	Contains     []string `yaml:"contains,omitempty" json:"contains,omitempty"`
	NotContains  []string `yaml:"notContains,omitempty" json:"notContains,omitempty"`
}

func (e Expect) empty() bool {
	return len(e.ToolsUsed) == 0 && len(e.ToolsNotUsed) == 0 &&
		len(e.Contains) == 0 && len(e.NotContains) == 0
}

// EvalCase is one input and the facts that must hold after it.
type EvalCase struct {
	Name  string `yaml:"name" json:"name"`
	Input string `yaml:"input" json:"input"`
	// Approve lists the tools this case lets run without a human. Everything else
	// is rejected, so a case that trips a destructive tool fails loudly instead of
	// quietly performing the action.
	Approve []string `yaml:"approve,omitempty" json:"approve,omitempty"`
	Expect  Expect   `yaml:"expect" json:"expect"`
}

// EvalSuite is the shape of evals.yaml.
type EvalSuite struct {
	Cases []EvalCase `yaml:"cases" json:"cases"`
}

// ParseEvals reads evals.yaml and rejects the mistakes that would otherwise pass
// silently: a case with no input, or one that asserts nothing.
func ParseEvals(raw []byte) (*EvalSuite, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return &EvalSuite{}, nil
	}
	// Decode the document node first. yaml.v3 happily decodes a bare scalar into a
	// struct without complaining, which would turn a file that is not even a
	// mapping into "no cases" — a suite that reports success because it never ran.
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("evals.yaml: %w", err)
	}
	root := &doc
	if doc.Kind == yaml.DocumentNode {
		if len(doc.Content) == 0 {
			return &EvalSuite{}, nil
		}
		root = doc.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("evals.yaml: expected a mapping with a cases: list")
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "cases" && root.Content[i+1].Kind != yaml.SequenceNode {
			return nil, fmt.Errorf("evals.yaml: cases must be a list")
		}
	}
	var suite EvalSuite
	if err := root.Decode(&suite); err != nil {
		return nil, fmt.Errorf("evals.yaml: %w", err)
	}
	seen := map[string]bool{}
	for i := range suite.Cases {
		c := &suite.Cases[i]
		if strings.TrimSpace(c.Input) == "" {
			return nil, fmt.Errorf("evals.yaml: case %d has no input", i+1)
		}
		if strings.TrimSpace(c.Name) == "" {
			c.Name = fmt.Sprintf("case-%d", i+1)
		}
		if seen[c.Name] {
			return nil, fmt.Errorf("evals.yaml: duplicate case name %q", c.Name)
		}
		seen[c.Name] = true
		if c.Expect.empty() {
			return nil, fmt.Errorf("evals.yaml: case %q asserts nothing, so it can only pass", c.Name)
		}
	}
	return &suite, nil
}

// CaseResult is one case's verdict.
type CaseResult struct {
	Name         string   `json:"name"`
	OK           bool     `json:"ok"`
	Failures     []string `json:"failures,omitempty"`
	Answer       string   `json:"answer,omitempty"`
	Tools        []string `json:"tools,omitempty"`
	LLMCalls     int      `json:"llmCalls,omitempty"`
	PromptTokens int      `json:"promptTokens,omitempty"`
	// OutputTokens includes thinking tokens, because a budget counts them.
	OutputTokens int `json:"outputTokens,omitempty"`
}

// EvalReport is the whole suite's verdict.
type EvalReport struct {
	Total  int          `json:"total"`
	Passed int          `json:"passed"`
	DurMs  int64        `json:"durMs"`
	Cases  []CaseResult `json:"cases"`
}

// Runner is the slice of *runner.Runner this package needs. A fake in a test is
// the only way to exercise a verdict without a live model.
type Runner interface {
	Run(ctx context.Context, userID, sessionID string, msg *genai.Content, cfg agent.RunConfig, opts ...runner.RunOption) iter.Seq2[*session.Event, error]
}

// EvalOptions tunes one suite run.
type EvalOptions struct {
	// SessionPrefix keeps cases from sharing history: a later case must not pass on
	// context an earlier one created. Each case appends its index.
	SessionPrefix string
	UserID        string
	// Confirm answers a tool-confirmation request. Nil means "approve exactly what
	// the case lists in approve", which is the rule both callers want; a caller that
	// passes its own function takes over that decision entirely.
	Confirm func(c EvalCase, tool string) bool
	// MaxHops bounds how many times one case may pause for confirmation.
	MaxHops int
}

// ErrNoCases lets a caller tell "nothing to run" from "everything passed".
var ErrNoCases = errors.New("no eval cases")

// NoCases reports whether a suite is empty.
func NoCases(suite *EvalSuite) bool { return suite == nil || len(suite.Cases) == 0 }

// RunEvals runs every case and reports the verdicts.
func RunEvals(ctx context.Context, r Runner, suite *EvalSuite, opt EvalOptions) (EvalReport, error) {
	if NoCases(suite) {
		return EvalReport{Cases: []CaseResult{}}, ErrNoCases
	}
	if opt.MaxHops <= 0 {
		opt.MaxHops = 4
	}
	if opt.UserID == "" {
		opt.UserID = "eval"
	}
	if opt.SessionPrefix == "" {
		opt.SessionPrefix = "eval"
	}

	start := time.Now()
	report := EvalReport{Total: len(suite.Cases)}
	for i, c := range suite.Cases {
		res := runCase(ctx, r, c, fmt.Sprintf("%s_%d", opt.SessionPrefix, i), opt)
		if res.OK {
			report.Passed++
		}
		report.Cases = append(report.Cases, res)
	}
	report.DurMs = time.Since(start).Milliseconds()
	return report, nil
}

func runCase(ctx context.Context, r Runner, c EvalCase, sessionID string, opt EvalOptions) CaseResult {
	res := CaseResult{Name: c.Name}
	content := genai.NewContentFromText(c.Input, genai.RoleUser)
	var answer strings.Builder

	for hop := 0; hop < opt.MaxHops; hop++ {
		pending, err := runCaseHop(ctx, r, sessionID, opt, content, &res, &answer)
		if err != nil {
			res.Failures = append(res.Failures, "run failed: "+err.Error())
			break
		}
		if len(pending) == 0 {
			break
		}
		parts := make([]*genai.Part, 0, len(pending))
		for _, pc := range pending {
			approved := approve(opt, c, pc.name)
			parts = append(parts, &genai.Part{FunctionResponse: &genai.FunctionResponse{
				Name:     toolconfirmation.FunctionCallName,
				ID:       pc.id,
				Response: map[string]any{"confirmed": approved},
			}})
		}
		content = &genai.Content{Role: string(genai.RoleUser), Parts: parts}
	}

	res.Answer = truncateRunes(answer.String(), 4000)
	res.Failures = append(res.Failures, checkExpect(c.Expect, res.Tools, answer.String())...)
	res.OK = len(res.Failures) == 0
	return res
}

// approve decides whether one tool call may run. A case has to name a destructive
// tool before it can happen, and a suite-level override replaces that rule.
func approve(opt EvalOptions, c EvalCase, tool string) bool {
	if opt.Confirm != nil {
		return opt.Confirm(c, tool)
	}
	return containsStr(c.Approve, tool)
}

type pendingCall struct {
	id   string
	name string
}

// runCaseHop streams one model call, collecting text, tool names and usage.
func runCaseHop(ctx context.Context, r Runner, sessionID string, opt EvalOptions, content *genai.Content, res *CaseResult, answer *strings.Builder) ([]pendingCall, error) {
	var pending []pendingCall
	for ev, err := range r.Run(ctx, opt.UserID, sessionID, content, agent.RunConfig{StreamingMode: agent.StreamingModeNone}) {
		if err != nil {
			return nil, err
		}
		if ev == nil {
			continue
		}
		if ev.ErrorMessage != "" {
			return nil, fmt.Errorf("%s", ev.ErrorMessage)
		}
		if ev.Content == nil {
			continue
		}
		if u := ev.UsageMetadata; u != nil {
			res.LLMCalls++
			res.PromptTokens += int(u.PromptTokenCount)
			res.OutputTokens += int(u.CandidatesTokenCount + u.ThoughtsTokenCount)
		}
		for _, part := range ev.Content.Parts {
			if part == nil {
				continue
			}
			switch {
			case part.FunctionCall != nil:
				fc := part.FunctionCall
				if fc.Name == toolconfirmation.FunctionCallName {
					orig, oerr := toolconfirmation.OriginalCallFrom(fc)
					if oerr != nil {
						continue
					}
					name := "unknown"
					if orig != nil && orig.Name != "" {
						name = orig.Name
					}
					pending = append(pending, pendingCall{id: fc.ID, name: name})
					continue
				}
				res.Tools = append(res.Tools, fc.Name)
			case part.Text != "" && !part.Thought:
				answer.WriteString(part.Text)
			}
		}
	}
	return pending, nil
}

func truncateRunes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return strings.ToValidUTF8(s[:max], "") + "…"
}

// checkExpect is the deterministic half: every assertion is a comparison against
// what actually happened in the stream.
func checkExpect(exp Expect, tools []string, answer string) []string {
	var failures []string
	for _, want := range exp.ToolsUsed {
		if !containsStr(tools, want) {
			failures = append(failures, fmt.Sprintf("tool %q was not called (called: %s)", want, orNone(tools)))
		}
	}
	for _, unwanted := range exp.ToolsNotUsed {
		if containsStr(tools, unwanted) {
			failures = append(failures, fmt.Sprintf("tool %q was called but must not be", unwanted))
		}
	}
	for _, want := range exp.Contains {
		if !strings.Contains(answer, want) {
			failures = append(failures, fmt.Sprintf("answer does not contain %q", want))
		}
	}
	for _, unwanted := range exp.NotContains {
		if strings.Contains(answer, unwanted) {
			failures = append(failures, fmt.Sprintf("answer contains %q, which it must not", unwanted))
		}
	}
	return failures
}

func containsStr(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func orNone(xs []string) string {
	if len(xs) == 0 {
		return "none"
	}
	return strings.Join(xs, ", ")
}
