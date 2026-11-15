package play

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool/toolconfirmation"
)

// confirmEvent builds the confirmation-request shape ADK emits for a tool call
// that needs a human: a wrapper function call carrying the original one.
func confirmEvent(tool, callID string) *session.Event {
	return callEvent(toolconfirmation.FunctionCallName, map[string]any{
		"originalFunctionCall": map[string]any{
			"name": tool,
			"args": map[string]any{"order_id": "A1"},
			"id":   "orig-1",
		},
	}, callID)
}

// TestParseEvalsRejectsCasesThatCannotFail is the guard against a suite that is
// green for the wrong reason: a case with nothing to assert can only pass.
func TestParseEvalsRejectsCasesThatCannotFail(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "no input",
			yaml: "cases:\n  - name: a\n    expect: {contains: [x]}\n",
			want: "no input",
		},
		{
			name: "asserts nothing",
			yaml: "cases:\n  - name: a\n    input: hi\n",
			want: "asserts nothing",
		},
		{
			name: "duplicate names",
			yaml: "cases:\n  - {name: a, input: hi, expect: {contains: [x]}}\n  - {name: a, input: ho, expect: {contains: [y]}}\n",
			want: "duplicate",
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
	// An empty file is a valid suite with no cases, which is how a project with the
	// file absent behaves.
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

// TestRunCaseAnswersConfirmations keeps a destructive tool from running inside a
// regression suite unless the case names it.
func TestRunCaseAnswersConfirmations(t *testing.T) {
	r := &scriptedRunner{scripts: [][]*session.Event{
		{confirmEvent("cancel_order", "c1")},
		{textEvent("已取消。")},
	}}
	sess := newTestSession(r)
	res := runCase(context.Background(), sess, testSlug, 0, EvalCase{
		Name: "no approval", Input: "取消订单 A1",
		Expect: Expect{Contains: []string{"取消"}},
	})
	// The confirmation was rejected, so the model was told so and the answer still
	// came back; the case passes on its own assertion but the tool never ran.
	if !res.OK {
		t.Fatalf("case failed: %+v", res.Failures)
	}
	second := r.inputs[1]
	if second == nil || len(second.Parts) != 1 || second.Parts[0].FunctionResponse == nil {
		t.Fatalf("no confirmation feedback: %+v", second)
	}
	if got := second.Parts[0].FunctionResponse.Response["confirmed"]; got != false {
		t.Fatalf("a case without approve must reject, got %v", got)
	}
}

func TestRunCaseWithApproval(t *testing.T) {
	r := &scriptedRunner{scripts: [][]*session.Event{
		{confirmEvent("cancel_order", "c1")},
		{textEvent("已取消。")},
	}}
	sess := newTestSession(r)
	res := runCase(context.Background(), sess, testSlug, 0, EvalCase{
		Name: "approved", Input: "取消订单 A1", Approve: []string{"cancel_order"},
		Expect: Expect{Contains: []string{"已取消"}},
	})
	if !res.OK {
		t.Fatalf("case failed: %+v", res.Failures)
	}
	if got := r.inputs[1].Parts[0].FunctionResponse.Response["confirmed"]; got != true {
		t.Fatalf("an approved tool was not confirmed: %v", got)
	}
}

// TestRunCaseReportsTheToolsCalled is what makes a failure actionable: the result
// names the tools that ran.
func TestRunCaseReportsTheToolsCalled(t *testing.T) {
	r := &scriptedRunner{scripts: [][]*session.Event{{
		callEvent("lookup", nil, "c1"),
		respEvent("lookup", "c1", map[string]any{"ok": true}),
		textEvent("好的。"),
	}}}
	sess := newTestSession(r)
	res := runCase(context.Background(), sess, testSlug, 0, EvalCase{
		Name: "wrong tool", Input: "查一下", Expect: Expect{ToolsUsed: []string{"search"}},
	})
	if res.OK {
		t.Fatal("a case expecting an uncalled tool passed")
	}
	if len(res.Tools) != 1 || res.Tools[0] != "lookup" {
		t.Fatalf("tools = %v", res.Tools)
	}
	if res.Stats == nil {
		t.Fatal("a case result should carry its cost")
	}
}
