package play

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// usageEvent is one model reply carrying provider usage, which is what the meter
// reads. Real providers set this on every non-streaming response.
func usageEvent(prompt, candidates, thoughts, cached int32) *session.Event {
	return &session.Event{LLMResponse: model.LLMResponse{
		Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: "ok"}}},
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:        prompt,
			CandidatesTokenCount:    candidates,
			ThoughtsTokenCount:      thoughts,
			CachedContentTokenCount: cached,
		},
	}}
}

// usageWithCall is one model reply that both carries usage and asks for a tool —
// the shape a real provider produces in a single response.
func usageWithCall(prompt, candidates, thoughts, cached int32, name, id string) *session.Event {
	return &session.Event{LLMResponse: model.LLMResponse{
		Content: &genai.Content{Role: "model", Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{Name: name, ID: id}},
		}},
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:        prompt,
			CandidatesTokenCount:    candidates,
			ThoughtsTokenCount:      thoughts,
			CachedContentTokenCount: cached,
		},
	}}
}

// TestConverseAttachesTurnStats is the point of the whole feature: after a turn,
// the UI must be able to say what it cost, and the numbers must match the events
// that were streamed.
func TestConverseAttachesTurnStats(t *testing.T) {
	r := &scriptedRunner{scripts: [][]*session.Event{{
		usageWithCall(120, 30, 10, 80, "lookup", "c1"),
		respEvent("lookup", "c1", map[string]any{"ok": true, "title": "hello"}),
		usageEvent(200, 20, 0, 0),
	}}}
	s := newTestSession(r)

	var events []Event
	s.Converse(context.Background(), Request{UserID: "u", SessionID: testSession, Message: "hi"}, func(ev Event) {
		events = append(events, ev)
	})

	var done *Event
	for i := range events {
		if events[i].Type == "done" {
			done = &events[i]
		}
	}
	if done == nil || done.Stats == nil {
		t.Fatalf("the closing event carries no account: %+v", events)
	}
	st := done.Stats
	if st.LLMCalls != 2 {
		t.Errorf("llmCalls = %d, want 2", st.LLMCalls)
	}
	if st.PromptTokens != 320 || st.OutputTokens != 60 || st.CachedTokens != 80 {
		t.Errorf("tokens = %d/%d cached %d, want 320/60 cached 80", st.PromptTokens, st.OutputTokens, st.CachedTokens)
	}
	if st.WallMs < 0 {
		t.Errorf("wall clock = %dms", st.WallMs)
	}
	if len(st.Tools) != 1 || st.Tools[0].Name != "lookup" || st.Tools[0].Calls != 1 || st.Tools[0].Errors != 0 {
		t.Errorf("tool account = %+v", st.Tools)
	}
}

// TestToolResultCarriesTheBody closes the debugging hole: before this, the only
// way to see what a tool returned was to read a file that stores a hash.
func TestToolResultCarriesTheBody(t *testing.T) {
	got, _, err := collect(t, fakeRunner{events: []*session.Event{
		callEvent("lookup", nil, "c1"),
		respEvent("lookup", "c1", map[string]any{"ok": true, "title": "hello"}),
	}})
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
		t.Fatalf("no tool_result: %+v", got)
	}
	if !strings.Contains(result.Result, `"title":"hello"`) {
		t.Fatalf("result body = %q, want the tool's reply", result.Result)
	}
}

// TestResultBodyIsCapped keeps one huge tool reply from becoming a megabyte SSE
// frame, and keeps the truncated text valid UTF-8 even when the cut lands inside
// a multi-byte rune.
func TestResultBodyIsCapped(t *testing.T) {
	big := map[string]any{"doc": strings.Repeat("订单", 3000)}
	got := resultBody(big)
	if len(got) > maxResultBytes+16 {
		t.Fatalf("body is %d bytes, want about %d", len(got), maxResultBytes)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatal("a truncated body must say so")
	}
	if !utf8.ValidString(got) {
		t.Fatal("truncation split a rune")
	}
	if resultBody(nil) != "" {
		t.Fatal("an empty result should stay empty")
	}
}

// TestStatsLineIsPersisted pins the file the operator can read later: one JSON
// line per turn, no message text.
func TestStatsLineIsPersisted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stats.jsonl")
	r := &scriptedRunner{scripts: [][]*session.Event{{usageEvent(50, 5, 0, 0)}}}
	s := newTestSession(r)
	s.statsPath = path
	s.stats = &statSink{}

	s.Converse(context.Background(), Request{UserID: "u", SessionID: "s1", Message: "hello"}, func(Event) {})

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("stats file: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1:\n%s", len(lines), raw)
	}
	var row struct {
		Slug      string `json:"slug"`
		SessionID string `json:"sessionId"`
		LLMCalls  int    `json:"llmCalls"`
		Prompt    int    `json:"promptTokens"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &row); err != nil {
		t.Fatalf("line is not JSON: %v", err)
	}
	if row.Slug != testSlug || row.SessionID != "s1" || row.LLMCalls != 1 || row.Prompt != 50 {
		t.Fatalf("row = %+v", row)
	}
	if strings.Contains(string(raw), "hello") {
		t.Fatal("the stats file must not record the message text")
	}
}

// TestMeterCountsFailures pins that a failed turn still produces an account: "it
// died after four calls" is exactly what someone needs to see.
func TestMeterCountsFailures(t *testing.T) {
	s := newTestSession(&errRunner{})
	s.statsPath = filepath.Join(t.TempDir(), "stats.jsonl")
	s.stats = &statSink{}

	var events []Event
	s.Converse(context.Background(), Request{UserID: "u", SessionID: testSession, Message: "hi"}, func(ev Event) {
		events = append(events, ev)
	})
	last := events[len(events)-1]
	if last.Type != "done" || last.Stats == nil {
		t.Fatalf("a failed turn must still close with an account: %+v", last)
	}
	if last.Stats.ErrCode != "WALL_CLOCK" {
		t.Fatalf("errCode = %q, want WALL_CLOCK", last.Stats.ErrCode)
	}
}

// TestMeterFirstByteOnlyCountsOnce keeps the "time to first token" honest: a
// second text event must not reset it.
func TestMeterFirstByteOnlyCountsOnce(t *testing.T) {
	m := newTurnMeter(time.Now())
	m.observe(Event{Type: "text"})
	first := m.firstByte
	m.observe(Event{Type: "text"})
	if !m.firstByte.Equal(first) {
		t.Fatal("first byte was overwritten by a later event")
	}
	if st := m.snapshot(time.Now()); st.FirstByteMs < 0 {
		t.Fatalf("firstByte = %dms", st.FirstByteMs)
	}
}

// TestDoneEventWireShape pins the field names the browser reads. There is no test
// on the other side of the SSE stream, so this is the only guard against renaming
// a JSON key and silently blanking the account panel.
func TestDoneEventWireShape(t *testing.T) {
	ok := true
	raw, err := json.Marshal(Event{
		Type:   "done",
		DurMs:  1234,
		Result: "body",
		Stats: &TurnStats{
			LLMCalls: 2, PromptTokens: 320, OutputTokens: 60, CachedTokens: 80,
			WallMs: 4100, FirstByteMs: 480,
			Tools: []ToolStat{{Name: "lookup", Calls: 1, Errors: 0, DurMs: 210}},
		},
		OK: &ok,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"stats":`, `"llmCalls":2`, `"promptTokens":320`, `"outputTokens":60`,
		`"cachedTokens":80`, `"wallMs":4100`, `"firstByteMs":480`,
		`"tools":[`, `"name":"lookup"`, `"calls":1`, `"durMs":210`, `"result":"body"`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("wire format lost %s\n%s", want, raw)
		}
	}
}

// TestStatsAggregatesAcrossTurns is the read side of the account: totals,
// percentiles and per-tool sums over the turns still on disk.
func TestStatsAggregatesAcrossTurns(t *testing.T) {
	svc := newService(t)
	defer svc.Close()
	path := filepath.Join(svc.st.RuntimeDir("demo"), "stats.jsonl")

	rows := []statLine{
		{TurnStats: TurnStats{LLMCalls: 2, PromptTokens: 100, OutputTokens: 20, CachedTokens: 80, WallMs: 100, FirstByteMs: 40,
			Tools: []ToolStat{{Name: "lookup", Calls: 1, DurMs: 30}}}},
		{TurnStats: TurnStats{LLMCalls: 1, PromptTokens: 300, OutputTokens: 10, WallMs: 200, FirstByteMs: 60,
			Tools: []ToolStat{{Name: "lookup", Calls: 2, Errors: 1, DurMs: 90}}}},
		{TurnStats: TurnStats{LLMCalls: 1, WallMs: 300, ErrCode: "WALL_CLOCK",
			Tools: []ToolStat{{Name: "search", Calls: 1, DurMs: 500}}}},
		{TurnStats: TurnStats{LLMCalls: 1, WallMs: 400}},
	}
	for _, r := range rows {
		svc.stats.write(path, r)
	}

	sum, err := svc.Stats("demo", 0)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Turns != 4 || sum.ErrTurns != 1 {
		t.Fatalf("turns = %d, errors = %d, want 4 and 1", sum.Turns, sum.ErrTurns)
	}
	if sum.PromptTokens != 400 || sum.OutputTokens != 30 || sum.CachedTokens != 80 {
		t.Fatalf("tokens = %d/%d cached %d", sum.PromptTokens, sum.OutputTokens, sum.CachedTokens)
	}
	if got := sum.CacheHitRate; got < 0.19 || got > 0.21 {
		t.Fatalf("cache hit rate = %v, want 80/400", got)
	}
	// walls 100,200,300,400 -> p50 200, p95 400, avg 250
	if sum.WallMs.P50 != 200 || sum.WallMs.P95 != 400 || sum.WallMs.Avg != 250 {
		t.Fatalf("wall = %+v, want p50 200 p95 400 avg 250", sum.WallMs)
	}
	if len(sum.Tools) != 2 || sum.Tools[0].Name != "search" {
		t.Fatalf("tools = %+v, want search first (slowest)", sum.Tools)
	}
	var lookup ToolSummary
	for _, ts := range sum.Tools {
		if ts.Name == "lookup" {
			lookup = ts
		}
	}
	if lookup.Calls != 3 || lookup.Errors != 1 || lookup.DurMs != 120 {
		t.Fatalf("lookup = %+v, want 3 calls / 1 error / 120ms", lookup)
	}
}

// TestStatsOnMissingFileIsEmpty: a project that never ran is not an error.
func TestStatsOnMissingFileIsEmpty(t *testing.T) {
	svc := newService(t)
	defer svc.Close()
	sum, err := svc.Stats("never-ran", 0)
	if err != nil {
		t.Fatalf("missing stats must not error: %v", err)
	}
	if sum.Turns != 0 || sum.Tools == nil {
		t.Fatalf("empty account = %+v", sum)
	}
}

// TestStatsSkipsTruncatedLines: a crash mid-write leaves half a line, and that
// must not void the turns that were recorded properly.
func TestStatsSkipsTruncatedLines(t *testing.T) {
	svc := newService(t)
	defer svc.Close()
	path := filepath.Join(svc.st.RuntimeDir("demo"), "stats.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"slug":"demo","llmCalls":1,"wallMs":100}` + "\n" + `{"slug":"demo","llmCa`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, err := svc.Stats("demo", 0)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Turns != 1 {
		t.Fatalf("turns = %d, want the one complete line", sum.Turns)
	}
}
