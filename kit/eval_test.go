package kit

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool/toolconfirmation"
	"google.golang.org/genai"
)

// scriptedRunner replays one script per Run call, so a test can model "asks, then
// answers once the confirmation comes back".
type scriptedRunner struct {
	scripts [][]*session.Event
	calls   int
	inputs  []*genai.Content
}

func (f *scriptedRunner) Run(_ context.Context, _, _ string, msg *genai.Content, _ agent.RunConfig, _ ...runner.RunOption) iter.Seq2[*session.Event, error] {
	f.inputs = append(f.inputs, msg)
	idx := f.calls
	f.calls++
	return func(yield func(*session.Event, error) bool) {
		if idx >= len(f.scripts) {
			return
		}
		for _, ev := range f.scripts[idx] {
			if !yield(ev, nil) {
				return
			}
		}
	}
}

func evalText(text string) *session.Event {
	return &session.Event{LLMResponse: model.LLMResponse{Content: &genai.Content{
		Role:  "model",
		Parts: []*genai.Part{{Text: text}},
	}}}
}

func evalCall(name, callID string, args map[string]any) *session.Event {
	return &session.Event{LLMResponse: model.LLMResponse{Content: &genai.Content{
		Role:  "model",
		Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: name, ID: callID, Args: args}}},
	}}}
}

// evalConfirm builds the shape ADK emits for a call that needs a human: a wrapper
// function call carrying the original one.
func evalConfirm(tool, callID string) *session.Event {
	return evalCall(toolconfirmation.FunctionCallName, callID, map[string]any{
		"originalFunctionCall": map[string]any{
			"name": tool,
			"args": map[string]any{"order_id": "A1"},
			"id":   "orig-1",
		},
	})
}

func evalResp(name, callID string, resp map[string]any) *session.Event {
	return &session.Event{LLMResponse: model.LLMResponse{Content: &genai.Content{
		Role:  "user",
		Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{Name: name, ID: callID, Response: resp}}},
	}}}
}

// TestParseEvalsRejectsCasesThatCannotFail is the guard against a suite that is
// green for the wrong reason: a case with nothing to assert can only pass.
func TestParseEvalsRejectsCasesThatCannotFail(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{"no input", "cases:\n  - name: a\n    expect: {contains: [x]}\n", "no input"},
		{"asserts nothing", "cases:\n  - name: a\n    input: hi\n", "asserts nothing"},
		{"root is not a mapping", "just-a-scalar", "expected a mapping"},
		{"cases is not a list", "cases: hello", "cases must be a list"},
		{
			"duplicate names",
			"cases:\n  - {name: a, input: hi, expect: {contains: [x]}}\n  - {name: a, input: ho, expect: {contains: [y]}}\n",
			"duplicate",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseEvals([]byte(tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

func TestParseEvalsAcceptsAndNamesUnnamedCases(t *testing.T) {
	suite, err := ParseEvals([]byte("cases:\n  - input: 帮我查订单\n    expect: {toolsUsed: [lookup]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(suite.Cases) != 1 || suite.Cases[0].Name != "case-1" {
		t.Fatalf("suite = %+v", suite.Cases)
	}
	empty, err := ParseEvals(nil)
	if err != nil || len(empty.Cases) != 0 {
		t.Fatalf("empty file: %+v / %v", empty, err)
	}
}

// TestCheckExpect covers the four assertion kinds in both directions. These are
// the only judgements the eval layer makes, so each one has to be right.
func TestCheckExpect(t *testing.T) {
	used := []string{"lookup", "lookup"}
	answer := "订单 A1 已发货，预计明天送达。"

	pass := Expect{
		ToolsUsed:    []string{"lookup"},
		ToolsNotUsed: []string{"cancel_order"},
		Contains:     []string{"A1", "已发货"},
		NotContains:  []string{"失败"},
	}
	if f := checkExpect(pass, used, answer); len(f) != 0 {
		t.Fatalf("unexpected failures: %v", f)
	}

	fail := Expect{
		ToolsUsed:    []string{"search"},
		ToolsNotUsed: []string{"lookup"},
		Contains:     []string{"退款"},
		NotContains:  []string{"明天"},
	}
	got := strings.Join(checkExpect(fail, used, answer), "\n")
	for _, want := range []string{
		`tool "search" was not called`,
		`tool "lookup" was called but must not be`,
		`does not contain "退款"`,
		`contains "明天"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing failure %q in:\n%s", want, got)
		}
	}
	// The message must say what was called, or the reader cannot fix the case.
	if !strings.Contains(got, "lookup, lookup") {
		t.Errorf("failure does not report the calls made:\n%s", got)
	}
}

// TestRunEvalsReportsToolsAndVerdict is the whole path: a case that expects a tool
// the agent called passes, and the report names what ran.
func TestRunEvalsReportsToolsAndVerdict(t *testing.T) {
	r := &scriptedRunner{scripts: [][]*session.Event{{
		evalCall("lookup", "c1", nil),
		evalResp("lookup", "c1", map[string]any{"ok": true}),
		evalText("订单 A1 已发货。"),
	}}}
	rep, err := RunEvals(context.Background(), r, &EvalSuite{Cases: []EvalCase{{
		Name: "查订单", Input: "查 A1",
		Expect: Expect{ToolsUsed: []string{"lookup"}, Contains: []string{"A1"}},
	}}}, EvalOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Total != 1 || rep.Passed != 1 {
		t.Fatalf("report = %+v", rep)
	}
	res := rep.Cases[0]
	if len(res.Tools) != 1 || res.Tools[0] != "lookup" {
		t.Fatalf("tools = %v", res.Tools)
	}
	if !strings.Contains(res.Answer, "A1") {
		t.Fatalf("answer = %q", res.Answer)
	}
}

// TestRunEvalsRejectsConfirmationsUnlessApproved keeps a destructive tool from
// running inside a regression suite unless the case names it.
func TestRunEvalsRejectsConfirmationsUnlessApproved(t *testing.T) {
	r := &scriptedRunner{scripts: [][]*session.Event{
		{evalConfirm("cancel_order", "c1")},
		{evalText("已取消。")},
	}}
	rep, err := RunEvals(context.Background(), r, &EvalSuite{Cases: []EvalCase{{
		Name: "no approval", Input: "取消订单 A1",
		Expect: Expect{Contains: []string{"取消"}},
	}}}, EvalOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Passed != 1 {
		t.Fatalf("case failed: %+v", rep.Cases)
	}
	second := r.inputs[1]
	if second == nil || len(second.Parts) != 1 || second.Parts[0].FunctionResponse == nil {
		t.Fatalf("no confirmation feedback: %+v", second)
	}
	if got := second.Parts[0].FunctionResponse.Response["confirmed"]; got != false {
		t.Fatalf("a case without approve must reject, got %v", got)
	}
}

func TestRunEvalsApprovesWhatTheCaseLists(t *testing.T) {
	r := &scriptedRunner{scripts: [][]*session.Event{
		{evalConfirm("cancel_order", "c1")},
		{evalText("已取消。")},
	}}
	rep, err := RunEvals(context.Background(), r, &EvalSuite{Cases: []EvalCase{{
		Name: "approved", Input: "取消订单 A1", Approve: []string{"cancel_order"},
		Expect: Expect{Contains: []string{"已取消"}},
	}}}, EvalOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Passed != 1 {
		t.Fatalf("case failed: %+v", rep.Cases)
	}
	if got := r.inputs[1].Parts[0].FunctionResponse.Response["confirmed"]; got != true {
		t.Fatalf("an approved tool was not confirmed: %v", got)
	}
}

// TestRunEvalsUsesOneSessionPerCase: a later case must not inherit context from an
// earlier one, or it would pass for the wrong reason.
func TestRunEvalsUsesOneSessionPerCase(t *testing.T) {
	var sessions []string
	r := &sessionRecorder{sessions: &sessions}
	_, err := RunEvals(context.Background(), r, &EvalSuite{Cases: []EvalCase{
		{Name: "a", Input: "x", Expect: Expect{Contains: []string{"ok"}}},
		{Name: "b", Input: "y", Expect: Expect{Contains: []string{"ok"}}},
	}}, EvalOptions{SessionPrefix: "eval"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 || sessions[0] == sessions[1] {
		t.Fatalf("sessions = %v, want two distinct ids", sessions)
	}
	if !strings.HasPrefix(sessions[0], "eval") {
		t.Fatalf("session id lost its prefix: %v", sessions)
	}
}

type sessionRecorder struct{ sessions *[]string }

func (s *sessionRecorder) Run(_ context.Context, _, sessionID string, _ *genai.Content, _ agent.RunConfig, _ ...runner.RunOption) iter.Seq2[*session.Event, error] {
	*s.sessions = append(*s.sessions, sessionID)
	return func(yield func(*session.Event, error) bool) {
		yield(evalText("ok"), nil)
	}
}

// TestRunEvalsEmptySuite: a project with no cases is not a failure, it is nothing
// to do — the caller decides what exit code that deserves.
func TestRunEvalsEmptySuite(t *testing.T) {
	_, err := RunEvals(context.Background(), &scriptedRunner{}, &EvalSuite{}, EvalOptions{})
	if !errors.Is(err, ErrNoCases) {
		t.Fatalf("err = %v, want ErrNoCases", err)
	}
}
