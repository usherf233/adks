package api

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
)

// converse runs a user turn to completion, resuming as many times as the run asks
// for a human decision.
//
// The loop is here rather than inline in the HTTP handler so it can be exercised
// without a live model: the resume path is the part most likely to break, and a
// real provider would never produce the exact pause a test needs.
func (s *Server) converse(ctx context.Context, rnr runnerLike, slug, userID, sessionID, message string, send func(PlayEvent)) {
	content := genai.NewContentFromText(message, genai.RoleUser)

	// A turn can pause for confirmation at most a few times; the cap keeps a
	// model that keeps asking from holding the connection forever.
	for hop := 0; hop < 4; hop++ {
		pending, runErr := s.runTurn(ctx, rnr, userID, sessionID, content, send)
		if runErr != nil {
			send(PlayEvent{Type: "error", Text: runErr.Error(), Code: codeFor(runErr)})
			send(PlayEvent{Type: "done"})
			return
		}
		if len(pending) == 0 {
			send(PlayEvent{Type: "done"})
			return
		}

		parts := make([]*genai.Part, 0, len(pending))
		for _, pc := range pending {
			// Registered before the event is sent, so a client that answers the
			// instant it reads callId can never arrive before the listener exists.
			ch := s.plays.register(pc.CallID, slug, sessionID)
			send(PlayEvent{Type: "confirm_required", Tool: pc.Tool, CallID: pc.CallID, Args: pc.Args})

			approved, ok := waitDecision(ctx, ch, confirmTimeout)
			if !ok {
				s.plays.drop(pc.CallID)
				no := false
				send(PlayEvent{Type: "tool_result", Tool: pc.Tool, CallID: pc.CallID, OK: &no, Code: "CONFIRM_TIMEOUT"})
			} else if !approved {
				no := false
				send(PlayEvent{Type: "tool_result", Tool: pc.Tool, CallID: pc.CallID, OK: &no, Code: "CONFIRM_REJECTED"})
			}
			parts = append(parts, &genai.Part{FunctionResponse: &genai.FunctionResponse{
				Name:     toolconfirmation.FunctionCallName,
				ID:       pc.CallID,
				Response: map[string]any{"confirmed": approved},
			}})
		}
		content = &genai.Content{Role: string(genai.RoleUser), Parts: parts}
	}
	send(PlayEvent{Type: "error", Text: "confirmation loop did not settle", Code: "CONFIRM_LOOP"})
	send(PlayEvent{Type: "done"})
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
func (s *Server) runTurn(ctx context.Context, rnr runnerLike, userID, sessionID string, content *genai.Content, send func(PlayEvent)) ([]pendingConfirm, error) {
	var pending []pendingConfirm
	for ev, err := range rnr.Run(ctx, userID, sessionID, content, agent.RunConfig{StreamingMode: agent.StreamingModeNone}) {
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
			send(PlayEvent{Type: "error", Text: ev.ErrorMessage, Code: "MODEL"})
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
				send(PlayEvent{Type: "tool_call", Tool: fc.Name, Args: fc.Args, CallID: fc.ID})

			case part.FunctionResponse != nil:
				fr := part.FunctionResponse
				if fr.Name == toolconfirmation.FunctionCallName {
					continue
				}
				ok := responseOK(fr.Response)
				send(PlayEvent{Type: "tool_result", Tool: fr.Name, CallID: fr.ID, OK: &ok})

			case part.Text != "" && !part.Thought:
				send(PlayEvent{Type: "text", Text: part.Text})
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
