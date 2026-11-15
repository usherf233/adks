package play

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Regression cases are the third layer of verification: build and vet prove the
// project can run, these prove it does the right thing.
//
// Only facts are asserted — which tools were called, what the answer contains.
// No model grades another model's output, for the same reason the manifest editor
// does not: a judgement that cannot be reproduced is not a check.

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
	var suite EvalSuite
	if err := yaml.Unmarshal(raw, &suite); err != nil {
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
	Name     string     `json:"name"`
	OK       bool       `json:"ok"`
	Failures []string   `json:"failures,omitempty"`
	Answer   string     `json:"answer,omitempty"`
	Tools    []string   `json:"tools,omitempty"`
	Stats    *TurnStats `json:"stats,omitempty"`
}

// EvalReport is the whole suite's verdict.
type EvalReport struct {
	Total  int          `json:"total"`
	Passed int          `json:"passed"`
	DurMs  int64        `json:"durMs"`
	Cases  []CaseResult `json:"cases"`
}

// maxEvalCases keeps one request from turning into an hour of model calls.
const maxEvalCases = 20

// RunEvals runs every case against the compiled project.
//
// Each case gets its own session id: cases must not see each other's history or
// a later case would pass on context the earlier one created.
func (s *Service) RunEvals(ctx context.Context, slug string, suite *EvalSuite) (EvalReport, error) {
	if suite == nil || len(suite.Cases) == 0 {
		return EvalReport{Cases: []CaseResult{}}, nil
	}
	if len(suite.Cases) > maxEvalCases {
		return EvalReport{}, fmt.Errorf("too many cases: %d, the limit is %d", len(suite.Cases), maxEvalCases)
	}
	start := time.Now()
	report := EvalReport{Total: len(suite.Cases)}
	for i, c := range suite.Cases {
		res := s.runEvalCase(ctx, slug, i, c)
		if res.OK {
			report.Passed++
		}
		report.Cases = append(report.Cases, res)
	}
	report.DurMs = time.Since(start).Milliseconds()
	return report, nil
}

func (s *Service) runEvalCase(ctx context.Context, slug string, idx int, c EvalCase) CaseResult {
	sess, err := s.Prepare(ctx, slug)
	if err != nil {
		return CaseResult{Name: c.Name, Failures: []string{err.Error()}}
	}
	sess.kind = "eval"
	defer sess.Close()
	return runCase(ctx, sess, slug, idx, c)
}

// runCase drives one case against an already prepared session. It is separate from
// Prepare so a case can be exercised without compiling a project.
func runCase(ctx context.Context, sess *Session, slug string, idx int, c EvalCase) CaseResult {
	res := CaseResult{Name: c.Name}

	// A unique session per case keeps the cases independent, and the eval_ prefix
	// keeps them out of the project's running account.
	sessionID := fmt.Sprintf("eval_%d_%d", time.Now().UnixNano(), idx)

	var (
		used   []string
		answer strings.Builder
		runErr error
	)
	sess.Converse(ctx, Request{UserID: "eval", SessionID: sessionID, Message: c.Input}, func(ev Event) {
		switch ev.Type {
		case "tool_call":
			used = append(used, ev.Tool)
		case "text":
			answer.WriteString(ev.Text)
		case "confirm_required":
			// Answer from inside the stream, the way the browser does. Anything the
			// case did not approve is rejected, so a destructive side effect has to
			// be written down before it can happen.
			sess.hub.deliver(ev.CallID, contains(c.Approve, ev.Tool), slug, sessionID)
		case "error":
			if runErr == nil {
				runErr = fmt.Errorf("%s: %s", ev.Code, ev.Text)
			}
		case "done":
			res.Stats = ev.Stats
		}
	})

	res.Tools = used
	res.Answer = truncate(answer.String(), maxResultBytes)
	if runErr != nil {
		res.Failures = append(res.Failures, "run failed: "+runErr.Error())
	}
	res.Failures = append(res.Failures, checkExpect(c.Expect, used, answer.String())...)
	res.OK = len(res.Failures) == 0
	return res
}

// checkExpect is the deterministic half: every assertion is a comparison against
// what actually happened in the stream.
func checkExpect(exp Expect, tools []string, answer string) []string {
	var failures []string
	for _, want := range exp.ToolsUsed {
		if !contains(tools, want) {
			failures = append(failures, fmt.Sprintf("tool %q was not called (called: %s)", want, orNone(tools)))
		}
	}
	for _, unwanted := range exp.ToolsNotUsed {
		if contains(tools, unwanted) {
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

func contains(xs []string, want string) bool {
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

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return strings.ToValidUTF8(s[:max], "") + "…"
}

// ErrNoEvals reports a run requested for a project without cases.
var ErrNoEvals = errors.New("no eval cases")
