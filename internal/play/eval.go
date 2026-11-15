package play

import (
	"context"
	"fmt"
	"time"

	"github.com/usherf233/adks/kit"
)

// The verdict logic lives in kit so the exported project can run the same cases
// with `agent -eval`. This file is the builder's driver: it compiles the project,
// runs the suite once, and maps the numbers into the shape the trial panel reads.

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

// RunEvals runs the suite against the compiled project.
//
// Cases do not share a session: a later case must not pass on context an earlier
// one created. Kit owns that rule, because the exported binary must apply the same
// one.
func (s *Service) RunEvals(ctx context.Context, slug string, suite *kit.EvalSuite) (EvalReport, error) {
	if kit.NoCases(suite) {
		return EvalReport{Cases: []CaseResult{}}, kit.ErrNoCases
	}
	if len(suite.Cases) > maxEvalCases {
		return EvalReport{}, fmt.Errorf("too many cases: %d, the limit is %d", len(suite.Cases), maxEvalCases)
	}
	sess, err := s.Prepare(ctx, slug)
	if err != nil {
		return EvalReport{}, err
	}
	// Eval turns are marked so they stay out of the project's running account.
	sess.kind = "eval"
	defer sess.Close()

	start := time.Now()
	report, err := kit.RunEvals(ctx, sess.rnr, suite, kit.EvalOptions{SessionPrefix: "eval"})
	if err != nil {
		return EvalReport{}, err
	}

	out := EvalReport{Total: report.Total, Passed: report.Passed, DurMs: time.Since(start).Milliseconds()}
	for _, c := range report.Cases {
		out.Cases = append(out.Cases, CaseResult{
			Name: c.Name, OK: c.OK, Failures: c.Failures, Answer: c.Answer, Tools: c.Tools,
			Stats: &TurnStats{
				LLMCalls:     c.LLMCalls,
				PromptTokens: c.PromptTokens,
				OutputTokens: c.OutputTokens,
				WallMs:       report.DurMs,
			},
		})
	}
	return out, nil
}
