package play

import (
	"context"
	"errors"
	"iter"
	"testing"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool/toolconfirmation"
	"google.golang.org/genai"

	"github.com/usherf233/adks/kit"
)

// fakeRunner replays a fixed event list. Using it instead of a live model is the
// only way to assert on the event mapping: a real run would never produce the
// exact sequence a test needs.
type fakeRunner struct {
	events []*session.Event
	err    error
}

func (f fakeRunner) Run(context.Context, string, string, *genai.Content, agent.RunConfig, ...runner.RunOption) iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		for _, ev := range f.events {
			if !yield(ev, nil) {
				return
			}
		}
		if f.err != nil {
			yield(nil, f.err)
		}
	}
}

// pacedRunner is fakeRunner with a delay between events, which is the only way to
// observe the tool-call duration: a run that replays instantly rounds to 0ms.
type pacedRunner struct {
	events []*session.Event
	gap    time.Duration
}

func (p pacedRunner) Run(context.Context, string, string, *genai.Content, agent.RunConfig, ...runner.RunOption) iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		for _, ev := range p.events {
			time.Sleep(p.gap)
			if !yield(ev, nil) {
				return
			}
		}
	}
}

func textEvent(text string) *session.Event {
	return &session.Event{LLMResponse: model.LLMResponse{Content: &genai.Content{
		Role:  "model",
		Parts: []*genai.Part{{Text: text}},
	}}}
}

func callEvent(name string, args map[string]any, id string) *session.Event {
	return &session.Event{LLMResponse: model.LLMResponse{Content: &genai.Content{
		Role:  "model",
		Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: name, Args: args, ID: id}}},
	}}}
}

func respEvent(name string, id string, resp map[string]any) *session.Event {
	return &session.Event{LLMResponse: model.LLMResponse{Content: &genai.Content{
		Role:  "user",
		Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{Name: name, ID: id, Response: resp}}},
	}}}
}

// newTestSession builds a Session with no compiled layer, which is enough for
// everything except Prepare. The slug and hub must agree with what the tests
// deliver decisions against.
func newTestSession(r kit.Runner) *Session {
	return &Session{slug: testSlug, rnr: r, hub: newPlayHub()}
}

const (
	testSlug    = "demo"
	testSession = "s"
)

func collect(t *testing.T, r kit.Runner) ([]Event, []pendingConfirm, error) {
	t.Helper()
	var got []Event
	s := newTestSession(r)
	pending, err := s.runTurn(context.Background(),
		Request{UserID: "u", SessionID: testSession},
		genai.NewContentFromText("hi", genai.RoleUser),
		newTurnMeter(time.Now()),
		func(ev Event) { got = append(got, ev) })
	return got, pending, err
}

func TestRunTurnMapsEvents(t *testing.T) {
	r := fakeRunner{events: []*session.Event{
		textEvent("好的，我查一下。"),
		callEvent("get_order", map[string]any{"order_id": "A1"}, "call-1"),
		respEvent("get_order", "call-1", map[string]any{"ok": true, "status": float64(200)}),
		textEvent("订单已发货。"),
		respEvent(toolconfirmation.FunctionCallName, "call-1", map[string]any{"confirmed": true}),
	}}
	got, pending, err := collect(t, r)
	if err != nil {
		t.Fatalf("runTurn: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("unexpected pending confirmations: %+v", pending)
	}
	want := []string{"text", "tool_call", "tool_result", "text"}
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Type != w {
			t.Fatalf("event %d type = %q, want %q (%+v)", i, got[i].Type, w, got[i])
		}
	}
	if got[1].Tool != "get_order" || got[1].CallID != "call-1" {
		t.Fatalf("tool_call lost its identity: %+v", got[1])
	}
	if got[2].OK == nil || !*got[2].OK {
		t.Fatalf("tool_result should report ok: %+v", got[2])
	}
}

// TestRunTurnReportsToolDuration pins that Event.DurMs is filled in. The field
// existed for a long time with nothing writing it, so the UI column that reads it
// was always empty.
func TestRunTurnReportsToolDuration(t *testing.T) {
	r := pacedRunner{
		events: []*session.Event{
			callEvent("lookup", map[string]any{"id": "1"}, "c1"),
			respEvent("lookup", "c1", map[string]any{"ok": true}),
		},
		gap: 5 * time.Millisecond,
	}
	got, _, err := collect(t, r)
	if err != nil {
		t.Fatal(err)
	}

	var result *Event
	for i := range got {
		if got[i].Type == "tool_result" {
			result = &got[i]
		}
	}
	if result == nil {
		t.Fatalf("no tool_result event: %+v", got)
	}
	if result.DurMs < 3 {
		t.Fatalf("tool_result duration = %dms, want at least the 5ms gap", result.DurMs)
	}
}

// TestRunTurnDurationsAreNotSharedAcrossCalls guards against a single accumulator:
// two calls must report their own durations, not the sum.
func TestRunTurnDurationsAreNotSharedAcrossCalls(t *testing.T) {
	r := pacedRunner{
		events: []*session.Event{
			callEvent("first", nil, "c1"),
			respEvent("first", "c1", map[string]any{"ok": true}),
			callEvent("second", nil, "c2"),
			respEvent("second", "c2", map[string]any{"ok": true}),
		},
		gap: 5 * time.Millisecond,
	}
	got, _, err := collect(t, r)
	if err != nil {
		t.Fatal(err)
	}
	var durs []int64
	for _, ev := range got {
		if ev.Type == "tool_result" {
			durs = append(durs, ev.DurMs)
		}
	}
	if len(durs) != 2 {
		t.Fatalf("got %d tool results, want 2", len(durs))
	}
	for i, d := range durs {
		if d > 20 {
			t.Fatalf("result %d reports %dms for a 5ms call: durations are accumulating", i, d)
		}
	}
}

func TestRunTurnSurfacesConfirmationRequest(t *testing.T) {
	r := fakeRunner{events: []*session.Event{
		callEvent(toolconfirmation.FunctionCallName, map[string]any{
			"originalFunctionCall": map[string]any{
				"name": "cancel_order",
				"args": map[string]any{"order_id": "A1"},
				"id":   "orig-1",
			},
			"toolConfirmation": map[string]any{"hint": "please approve"},
		}, "conf-1"),
	}}
	got, pending, err := collect(t, r)
	if err != nil {
		t.Fatalf("runTurn: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("runTurn must not emit the confirmation itself, Converse does: %+v", got)
	}
	if len(pending) != 1 || pending[0].CallID != "conf-1" || pending[0].Tool != "cancel_order" {
		t.Fatalf("confirmation not tracked: %+v", pending)
	}
	if pending[0].Args["order_id"] != "A1" {
		t.Fatalf("args not forwarded to the human: %+v", pending[0].Args)
	}
}

// scriptedRunner replays one script per Run call, so a test can model "asks, then
// answers once approved".
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

func TestConverseResumesAfterApproval(t *testing.T) {
	r := &scriptedRunner{scripts: [][]*session.Event{
		// First hop: the model asks to cancel an order and stops.
		{callEvent(toolconfirmation.FunctionCallName, map[string]any{
			"originalFunctionCall": map[string]any{
				"name": "cancel_order", "args": map[string]any{"order_id": "A1"}, "id": "orig-1",
			},
		}, "conf-1")},
		// Second hop: after approval the model answers.
		{textEvent("已取消。")},
	}}
	s := newTestSession(r)

	var events []Event
	s.Converse(context.Background(), Request{UserID: "u", SessionID: testSession, Message: "取消订单 A1"}, func(ev Event) {
		events = append(events, ev)
		// Answer the confirmation from inside the stream, the way the browser does.
		if ev.Type == "confirm_required" {
			s.hub.deliver(ev.CallID, true, testSlug, testSession)
		}
	})

	if r.calls != 2 {
		t.Fatalf("expected the run to resume once, got %d calls", r.calls)
	}
	// The resume payload must be a function response for the confirmation call.
	second := r.inputs[1]
	if second == nil || len(second.Parts) != 1 || second.Parts[0].FunctionResponse == nil {
		t.Fatalf("resume content is not a function response: %+v", second)
	}
	fr := second.Parts[0].FunctionResponse
	if fr.Name != toolconfirmation.FunctionCallName || fr.ID != "conf-1" || fr.Response["confirmed"] != true {
		t.Fatalf("resume payload wrong: %+v", fr)
	}

	types := make([]string, len(events))
	for i, e := range events {
		types[i] = e.Type
	}
	want := []string{"confirm_required", "text", "done"}
	if len(types) != len(want) {
		t.Fatalf("events = %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("events = %v, want %v", types, want)
		}
	}
}

func TestConverseRejectionIsReportedAndFedBack(t *testing.T) {
	r := &scriptedRunner{scripts: [][]*session.Event{
		{callEvent(toolconfirmation.FunctionCallName, map[string]any{
			"originalFunctionCall": map[string]any{"name": "cancel_order", "args": map[string]any{}, "id": "o"},
		}, "conf-2")},
		{textEvent("好的，我不执行。")},
	}}
	s := newTestSession(r)
	var events []Event
	s.Converse(context.Background(), Request{UserID: "u", SessionID: testSession, Message: "取消"}, func(ev Event) {
		events = append(events, ev)
		if ev.Type == "confirm_required" {
			s.hub.deliver(ev.CallID, false, testSlug, testSession)
		}
	})

	var rejected bool
	for _, e := range events {
		if e.Type == "tool_result" && e.Code == "CONFIRM_REJECTED" && e.OK != nil && !*e.OK {
			rejected = true
		}
	}
	if !rejected {
		t.Fatalf("a rejected call must be reported as a failed tool result: %+v", events)
	}
	if got := r.inputs[1].Parts[0].FunctionResponse.Response["confirmed"]; got != false {
		t.Fatalf("rejection must be fed back to the model, got %v", got)
	}
}

func TestConverseStopsWhenBudgetRunsOut(t *testing.T) {
	s := newTestSession(&errRunner{})
	var events []Event
	s.Converse(context.Background(), Request{UserID: "u", SessionID: testSession, Message: "hi"}, func(ev Event) {
		events = append(events, ev)
	})
	if len(events) < 2 || events[0].Type != "error" || events[len(events)-1].Type != "done" {
		t.Fatalf("a failing run must end with an error and a done event: %+v", events)
	}
	if events[0].Code != "WALL_CLOCK" {
		t.Fatalf("a budget failure must carry the WALL_CLOCK code, got %+v", events[0])
	}
}

type errRunner struct{}

func (errRunner) Run(context.Context, string, string, *genai.Content, agent.RunConfig, ...runner.RunOption) iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		yield(nil, kit.ErrLLMLimit)
	}
}

func TestRunTurnReportsModelErrorEvent(t *testing.T) {
	ev := textEvent("")
	ev.ErrorMessage = "quota exceeded"
	got, _, err := collect(t, fakeRunner{events: []*session.Event{ev}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Type != "error" || got[0].Code != "MODEL" {
		t.Fatalf("expected a MODEL error event, got %+v", got)
	}
}

func TestRunTurnReturnsRunnerError(t *testing.T) {
	_, _, err := collect(t, fakeRunner{err: errors.New("boom")})
	if err == nil || err.Error() != "boom" {
		t.Fatalf("runner error must propagate, got %v", err)
	}
}

func TestPlayHubDeliversOnlyOnce(t *testing.T) {
	h := newPlayHub()
	ch := h.register("c1", "p", "s")
	if !h.deliver("c1", true, "p", "s") {
		t.Fatal("expected delivery to a registered call")
	}
	if v := <-ch; !v {
		t.Fatal("expected approval to arrive")
	}
	if h.deliver("c1", true, "p", "s") {
		t.Fatal("a second delivery must not find a listener")
	}
	if h.deliver("nope", true, "p", "s") {
		t.Fatal("unknown call id must not deliver")
	}
}

// TestPlayHubRefusesForeignRun is the isolation rule: a decision must come from
// the run that asked. Otherwise two open chat panels could approve each other's
// tool calls.
func TestPlayHubRefusesForeignRun(t *testing.T) {
	h := newPlayHub()
	h.register("c1", "p", "s1")
	if h.deliver("c1", true, "p", "s2") {
		t.Fatal("a decision from another session was accepted")
	}
	if h.deliver("c1", true, "other", "s1") {
		t.Fatal("a decision from another project was accepted")
	}
	if !h.deliver("c1", true, "p", "s1") {
		t.Fatal("the owning run could not deliver")
	}
}

func TestResponseOK(t *testing.T) {
	cases := []struct {
		resp map[string]any
		want bool
	}{
		{map[string]any{"ok": true}, true},
		{map[string]any{"ok": false}, false},
		{map[string]any{"error": "boom"}, false},
		{map[string]any{"content": "fine"}, true},
	}
	for _, c := range cases {
		if got := responseOK(c.resp); got != c.want {
			t.Errorf("responseOK(%v) = %v, want %v", c.resp, got, c.want)
		}
	}
}

// TestSessionCloseReleasesOnce keeps the refcount contract: a double Close must
// not decrement twice, which would let the cache close a layer someone else is
// still streaming.
func TestSessionCloseReleasesOnce(t *testing.T) {
	releases := 0
	s := &Session{release: func() { releases++ }}
	s.Close()
	s.Close()
	if releases != 1 {
		t.Fatalf("release called %d times, want 1", releases)
	}
}

// TestPlayHubCloseDropsPending is the shutdown contract: after Close, a late
// click must not resurrect a decision nobody is waiting for.
func TestPlayHubCloseDropsPending(t *testing.T) {
	h := newPlayHub()
	h.register("c1", "project-a", "session-a")
	h.Close()
	if h.deliver("c1", true, "project-a", "session-a") {
		t.Fatal("a closed hub still delivered a decision")
	}
}
