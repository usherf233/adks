package play

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool/toolconfirmation"
	"google.golang.org/genai"

	"github.com/usherf233/adks/kit"
	"github.com/usherf233/adks/manifest"
)

// Event is one step of a trial run. The JSON tags are the wire format the web UI
// consumes, so they are part of the API: type is one of
// text | tool_call | tool_result | confirm_required | error | done.
type Event struct {
	Type   string         `json:"type"`
	Text   string         `json:"text,omitempty"`
	Tool   string         `json:"tool,omitempty"`
	CallID string         `json:"callId,omitempty"`
	Args   map[string]any `json:"args,omitempty"`
	OK     *bool          `json:"ok,omitempty"`
	Code   string         `json:"code,omitempty"`
	// DurMs carries the measured duration for tool_result, error and done.
	DurMs int64 `json:"durMs,omitempty"`
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
	slug    string
	rnr     runnerLike
	project *manifest.Project
	hub     *playHub
	release func()
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
	turnStart := time.Now()

	// A turn can pause for confirmation at most a few times; the cap keeps a
	// model that keeps asking from holding the connection forever.
	for hop := 0; hop < 4; hop++ {
		pending, runErr := s.runTurn(ctx, req, content, send)
		if runErr != nil {
			send(Event{Type: "error", Text: runErr.Error(), Code: codeFor(runErr), DurMs: time.Since(turnStart).Milliseconds()})
			send(Event{Type: "done"})
			return
		}
		if len(pending) == 0 {
			send(Event{Type: "done", DurMs: time.Since(turnStart).Milliseconds()})
			return
		}

		parts := make([]*genai.Part, 0, len(pending))
		for _, pc := range pending {
			// Registered before the event is sent, so a client that answers the
			// instant it reads callId can never arrive before the listener exists.
			ch := s.hub.register(pc.CallID, s.slug, req.SessionID)
			send(Event{Type: "confirm_required", Tool: pc.Tool, CallID: pc.CallID, Args: pc.Args})

			asked := time.Now()
			approved, ok := waitDecision(ctx, ch, confirmTimeout)
			if !ok {
				s.hub.drop(pc.CallID)
				no := false
				send(Event{Type: "tool_result", Tool: pc.Tool, CallID: pc.CallID, OK: &no, Code: "CONFIRM_TIMEOUT", DurMs: time.Since(asked).Milliseconds()})
			} else if !approved {
				no := false
				send(Event{Type: "tool_result", Tool: pc.Tool, CallID: pc.CallID, OK: &no, Code: "CONFIRM_REJECTED", DurMs: time.Since(asked).Milliseconds()})
			}
			parts = append(parts, &genai.Part{FunctionResponse: &genai.FunctionResponse{
				Name:     toolconfirmation.FunctionCallName,
				ID:       pc.CallID,
				Response: map[string]any{"confirmed": approved},
			}})
		}
		content = &genai.Content{Role: string(genai.RoleUser), Parts: parts}
	}
	send(Event{Type: "error", Text: "confirmation loop did not settle", Code: "CONFIRM_LOOP", DurMs: time.Since(turnStart).Milliseconds()})
	send(Event{Type: "done", DurMs: time.Since(turnStart).Milliseconds()})
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
func (s *Session) runTurn(ctx context.Context, req Request, content *genai.Content, send func(Event)) ([]pendingConfirm, error) {
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
				send(Event{Type: "tool_result", Tool: fr.Name, CallID: fr.ID, OK: &ok, DurMs: elapsed(fr.ID)})

			case part.Text != "" && !part.Thought:
				send(Event{Type: "text", Text: part.Text})
			}
		}
	}
	return pending, nil
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
	case errors.Is(err, kit.ErrLLMLimit):
		return "WALL_CLOCK"
	case errors.Is(err, kit.ErrLoopDetected):
		return "LOOP"
	case errors.Is(err, context.Canceled):
		return "CANCELED"
	}
	return "ERROR"
}
