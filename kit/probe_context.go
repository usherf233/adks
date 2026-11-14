package kit

import (
	"context"
	"iter"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// probeContext satisfies agent.ReadonlyContext for out-of-band calls such as a
// probe. Only the context methods are meaningful; a probe has no session.
type probeContext struct{ context.Context }

func (probeContext) UserContent() *genai.Content { return nil }
func (probeContext) InvocationID() string        { return "probe" }
func (probeContext) AgentName() string           { return "probe" }

// ReadonlyState returns an empty state rather than nil: a toolset may read state
// while listing tools (deferred loading does), and a nil interface would panic
// inside ADK instead of returning an empty result.
func (probeContext) ReadonlyState() session.ReadonlyState { return emptyState{} }
func (probeContext) UserID() string                       { return "probe" }
func (probeContext) AppName() string                      { return "probe" }
func (probeContext) SessionID() string                    { return "probe" }
func (probeContext) Branch() string                       { return "" }

// emptyState satisfies session.ReadonlyState with no keys.
type emptyState struct{}

func (emptyState) Get(string) (any, error) { return nil, session.ErrStateKeyNotExist }
func (emptyState) All() iter.Seq2[string, any] {
	return func(func(string, any) bool) {}
}

var _ agent.ReadonlyContext = probeContext{}
