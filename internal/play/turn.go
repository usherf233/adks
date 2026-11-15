package play

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"sort"
	"strings"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool/toolconfirmation"
	"google.golang.org/genai"

	"github.com/usherf233/adks/kit"
	"github.com/usherf233/adks/manifest"
)

// maxResultBytes caps the tool result body forwarded to the UI. The body is for
// debugging on the operator's own screen; the audit log still records only a
// hash, so a large payload never accumulates in a file.
const maxResultBytes = 4000

// Event is one step of a trial run. The JSON tags are the wire format the web UI
// consumes, so they are part of the API: type is one of
// text | tool_call | tool_result | confirm_required | error | done.
type Event struct {
	Type   string         `json:"type"`
	Text   string         `json:"text,omitempty"`
	Tool   string         `json:"tool,omitempty"`
	CallID string         `json:"callId,omitempty"`
	Args   map[string]any `json:"args,omitempty"`
	// Result is the tool's reply, truncated. Without it the only way to find out
	// what a tool actually returned was to read audit.jsonl, which stores a hash.
	Result string `json:"result,omitempty"`
	OK     *bool  `json:"ok,omitempty"`
	Code   string `json:"code,omitempty"`
	// DurMs carries the measured duration for tool_result, error and done.
	DurMs int64 `json:"durMs,omitempty"`
	// Stats is attached to the closing event only.
	Stats *TurnStats `json:"stats,omitempty"`
}

// ToolStat is what one tool cost during a turn.
type ToolStat struct {
	Name   string `json:"name"`
	Calls  int    `json:"calls"`
	Errors int    `json:"errors"`
	DurMs  int64  `json:"durMs"`
}

// TurnStats is the account of one turn: what it cost and how long it felt.
//
// Every number here is measured, not estimated. The static estimate the editor
// shows before a run answers "how big is the prompt"; this answers "what did it
// actually cost", which is the only question that can justify a model change.
type TurnStats struct {
	LLMCalls     int `json:"llmCalls"`
	PromptTokens int `json:"promptTokens"`
	OutputTokens int `json:"outputTokens"`
	// CachedTokens is the part of the prompt the provider served from its cache.
	// It is the one number that proves the prefix-stability work is paying off.
	CachedTokens int        `json:"cachedTokens,omitempty"`
	WallMs       int64      `json:"wallMs"`
	FirstByteMs  int64      `json:"firstByteMs,omitempty"`
	Tools        []ToolStat `json:"tools,omitempty"`
	ErrCode      string     `json:"errCode,omitempty"`
}

// turnMeter accumulates the turn's account by watching the same events the UI is
// sent, so the numbers reported can never drift from what the user saw.
type turnMeter struct {
	start     time.Time
	firstByte time.Time

	llmCalls     int
	promptTokens int
	outputTokens int
	cachedTokens int
	errCode      string

	open  map[string]time.Time
	tools map[string]*ToolStat
}

func newTurnMeter(start time.Time) *turnMeter {
	return &turnMeter{
		start: start,
		open:  map[string]time.Time{},
		tools: map[string]*ToolStat{},
	}
}

// observe records a play event. It is called for every event the UI receives.
func (m *turnMeter) observe(ev Event) {
	switch ev.Type {
	case "text", "tool_call":
		if m.firstByte.IsZero() {
			m.firstByte = time.Now()
		}
		if ev.Type == "tool_call" {
			m.open[ev.CallID] = time.Now()
		}
	case "tool_result":
		t := m.tool(ev.Tool)
		t.Calls++
		if ev.OK != nil && !*ev.OK {
			t.Errors++
		}
		if started, ok := m.open[ev.CallID]; ok {
			t.DurMs += time.Since(started).Milliseconds()
			delete(m.open, ev.CallID)
		}
	case "error":
		m.errCode = ev.Code
	}
}

// observeLLM records one raw ADK event, which is the only place usage metadata
// exists: the play events are a projection and do not carry it.
func (m *turnMeter) observeLLM(ev *session.Event) {
	if ev == nil || ev.Content == nil {
		return
	}
	if u := ev.UsageMetadata; u != nil {
		m.llmCalls++
		m.promptTokens += int(u.PromptTokenCount)
		m.outputTokens += int(u.CandidatesTokenCount + u.ThoughtsTokenCount)
		m.cachedTokens += int(u.CachedContentTokenCount)
		return
	}
	// A provider that reports no usage still made a call: count the response.
	for _, p := range ev.Content.Parts {
		if p != nil && (p.Text != "" || p.FunctionCall != nil) {
			m.llmCalls++
			return
		}
	}
}

func (m *turnMeter) tool(name string) *ToolStat {
	if name == "" {
		name = "unknown"
	}
	if t, ok := m.tools[name]; ok {
		return t
	}
	t := &ToolStat{Name: name}
	m.tools[name] = t
	return t
}

func (m *turnMeter) snapshot(now time.Time) *TurnStats {
	st := &TurnStats{
		LLMCalls:     m.llmCalls,
		PromptTokens: m.promptTokens,
		OutputTokens: m.outputTokens,
		CachedTokens: m.cachedTokens,
		WallMs:       now.Sub(m.start).Milliseconds(),
		ErrCode:      m.errCode,
	}
	if !m.firstByte.IsZero() {
		st.FirstByteMs = m.firstByte.Sub(m.start).Milliseconds()
	}
	for _, t := range m.tools {
		st.Tools = append(st.Tools, *t)
	}
	// Slowest first: the tool that is worth fixing is at the top.
	sort.Slice(st.Tools, func(i, j int) bool { return st.Tools[i].DurMs > st.Tools[j].DurMs })
	return st
}

// Request is one turn from the chat panel.
type Request struct {
	UserID    string
	SessionID string
	Message   string
}

// Session is a compiled project pinned for one turn.
//
// Close must be called when the turn ends, whatever the outcome: it releases the
// cache reference that keeps the MCP connections and session database alive.
type Session struct {
	slug      string
	rnr       runnerLike
	project   *manifest.Project
	hub       *playHub
	release   func()
	statsPath string
	stats     *statSink
	// kind is "eval" for a regression run, empty for a human turn. It keeps the
	// project's running account free of cases the user did not type.
	kind string
}

// WallClock is the budget for the whole turn, from the manifest's policy.
func (s *Session) WallClock() time.Duration {
	if s.project != nil {
		if d := time.Duration(s.project.Spec.Policy.Limits.MaxWallClockSec) * time.Second; d > 0 {
			return d
		}
	}
	return defaultWallClock
}

// Close releases the cache reference. It is safe to call once per turn.
func (s *Session) Close() {
	if s.release != nil {
		s.release()
		s.release = nil
	}
}

// Converse runs a user turn to completion, resuming as many times as the run asks
// for a human decision.
//
// The loop is here rather than in the HTTP handler so it can be exercised without
// a live model: the resume path is the part most likely to break, and a real
// provider would never produce the exact pause a test needs.
func (s *Session) Converse(ctx context.Context, req Request, send func(Event)) {
	content := genai.NewContentFromText(req.Message, genai.RoleUser)
	meter := newTurnMeter(time.Now())

	emit := func(ev Event) {
		meter.observe(ev)
		send(ev)
	}
	// finish closes the turn: the account rides on the done event and is written to
	// the project's stats file. A turn that ended badly still gets an account,
	// because "it failed after 4 calls" is exactly what someone needs to see.
	finish := func() {
		st := meter.snapshot(time.Now())
		emit(Event{Type: "done", DurMs: st.WallMs, Stats: st})
		s.recordStats(req, st)
	}

	// A turn can pause for confirmation at most a few times; the cap keeps a
	// model that keeps asking from holding the connection forever.
	for hop := 0; hop < 4; hop++ {
		pending, runErr := s.runTurn(ctx, req, content, meter, emit)
		if runErr != nil {
			emit(Event{Type: "error", Text: runErr.Error(), Code: codeFor(runErr)})
			finish()
			return
		}
		if len(pending) == 0 {
			finish()
			return
		}

		parts := make([]*genai.Part, 0, len(pending))
		for _, pc := range pending {
			// Registered before the event is sent, so a client that answers the
			// instant it reads callId can never arrive before the listener exists.
			ch := s.hub.register(pc.CallID, s.slug, req.SessionID)
			emit(Event{Type: "confirm_required", Tool: pc.Tool, CallID: pc.CallID, Args: pc.Args})

			asked := time.Now()
			approved, ok := waitDecision(ctx, ch, confirmTimeout)
			if !ok {
				s.hub.drop(pc.CallID)
				no := false
				emit(Event{Type: "tool_result", Tool: pc.Tool, CallID: pc.CallID, OK: &no, Code: "CONFIRM_TIMEOUT", DurMs: time.Since(asked).Milliseconds()})
			} else if !approved {
				no := false
				emit(Event{Type: "tool_result", Tool: pc.Tool, CallID: pc.CallID, OK: &no, Code: "CONFIRM_REJECTED", DurMs: time.Since(asked).Milliseconds()})
			}
			parts = append(parts, &genai.Part{FunctionResponse: &genai.FunctionResponse{
				Name:     toolconfirmation.FunctionCallName,
				ID:       pc.CallID,
				Response: map[string]any{"confirmed": approved},
			}})
		}
		content = &genai.Content{Role: string(genai.RoleUser), Parts: parts}
	}
	emit(Event{Type: "error", Text: "confirmation loop did not settle", Code: "CONFIRM_LOOP"})
	finish()
}

// runnerLike is the slice of *runner.Runner this file needs, which keeps runTurn
// testable without a live model.
type runnerLike interface {
	Run(ctx context.Context, userID, sessionID string, msg *genai.Content, cfg agent.RunConfig, opts ...runner.RunOption) iter.Seq2[*session.Event, error]
}

type pendingConfirm struct {
	CallID string
	Tool   string
	Args   map[string]any
}

// runTurn streams one runner invocation and returns the confirmations it asked
// for.
func (s *Session) runTurn(ctx context.Context, req Request, content *genai.Content, meter *turnMeter, send func(Event)) ([]pendingConfirm, error) {
	var pending []pendingConfirm
	turnStart := time.Now()
	// Tool calls and their results arrive as separate events, so the duration the
	// UI shows has to be paired here. The key is the call ID, which ADK keeps
	// stable between the call and its response.
	started := map[string]time.Time{}
	elapsed := func(callID string) int64 {
		t, ok := started[callID]
		if !ok {
			return 0
		}
		delete(started, callID)
		return time.Since(t).Milliseconds()
	}
	var rnr runnerLike = s.rnr
	for ev, err := range rnr.Run(ctx, req.UserID, req.SessionID, content, agent.RunConfig{StreamingMode: agent.StreamingModeNone}) {
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
				return nil, fmt.Errorf("%w: %s", kit.ErrLLMLimit, "wall clock exhausted")
			}
			return nil, err
		}
		if ev == nil {
			continue
		}
		if ev.ErrorMessage != "" {
			send(Event{Type: "error", Text: ev.ErrorMessage, Code: "MODEL", DurMs: time.Since(turnStart).Milliseconds()})
			continue
		}
		if ev.Content == nil {
			continue
		}
		// Usage metadata only exists on the raw event: the play events are a
		// projection and never carry it.
		meter.observeLLM(ev)
		for _, part := range ev.Content.Parts {
			if part == nil {
				continue
			}
			switch {
			case part.FunctionCall != nil:
				fc := part.FunctionCall
				if fc.Name == toolconfirmation.FunctionCallName {
					orig, err := toolconfirmation.OriginalCallFrom(fc)
					if err != nil {
						continue
					}
					tool := "unknown"
					var args map[string]any
					if orig != nil {
						tool = orig.Name
						args = orig.Args
					}
					pending = append(pending, pendingConfirm{CallID: fc.ID, Tool: tool, Args: args})
					continue
				}
				started[fc.ID] = time.Now()
				send(Event{Type: "tool_call", Tool: fc.Name, Args: fc.Args, CallID: fc.ID})

			case part.FunctionResponse != nil:
				fr := part.FunctionResponse
				if fr.Name == toolconfirmation.FunctionCallName {
					continue
				}
				ok := responseOK(fr.Response)
				send(Event{
					Type: "tool_result", Tool: fr.Name, CallID: fr.ID, OK: &ok,
					DurMs: elapsed(fr.ID), Result: resultBody(fr.Response),
				})

			case part.Text != "" && !part.Thought:
				send(Event{Type: "text", Text: part.Text})
			}
		}
	}
	return pending, nil
}

// resultBody renders a tool reply for the UI, capped.
//
// A tool that returns a large document would otherwise put megabytes into one SSE
// frame. The audit log keeps only a hash of the arguments and none of the result,
// so this is the only place a body appears — and it stays on the operator's own
// screen.
func resultBody(resp map[string]any) string {
	if len(resp) == 0 {
		return ""
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		return ""
	}
	if len(raw) <= maxResultBytes {
		return string(raw)
	}
	// Trim at a rune boundary so the truncated text is still valid UTF-8.
	cut := strings.ToValidUTF8(string(raw[:maxResultBytes]), "")
	return cut + "…"
}

// recordStats appends the turn's account. A failure to write must not fail the
// turn: these numbers are for the operator, not for the run.
func (s *Session) recordStats(req Request, st *TurnStats) {
	if s.stats == nil || s.statsPath == "" || st == nil {
		return
	}
	s.stats.write(s.statsPath, statLine{
		TS:        time.Now(),
		Slug:      s.slug,
		SessionID: req.SessionID,
		UserID:    req.UserID,
		Kind:      s.kind,
		TurnStats: *st,
	})
}

// responseOK reads the ok flag a tool put in its result. A tool that returns no
// flag is assumed to have succeeded: ADK only surfaces a result on success.
func responseOK(resp map[string]any) bool {
	if v, ok := resp["ok"].(bool); ok {
		return v
	}
	if _, hasErr := resp["error"]; hasErr {
		return false
	}
	return true
}

func waitDecision(ctx context.Context, ch chan bool, timeout time.Duration) (bool, bool) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case v := <-ch:
		return v, true
	case <-timer.C:
		return false, false
	case <-ctx.Done():
		return false, false
	}
}

// codeFor maps an error to the code the UI shows.
func codeFor(err error) string {
	switch {
	case errors.Is(err, kit.ErrTokenBudget):
		return "TOKEN_BUDGET"
	case errors.Is(err, kit.ErrLLMLimit):
		return "WALL_CLOCK"
	case errors.Is(err, kit.ErrLoopDetected):
		return "LOOP"
	case errors.Is(err, context.Canceled):
		return "CANCELED"
	}
	return "ERROR"
}
