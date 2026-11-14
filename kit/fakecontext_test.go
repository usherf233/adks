package kit

import (
	"context"
	"iter"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/memory"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool/toolconfirmation"
	"google.golang.org/genai"
)

// fakeCtx is a minimal agent.Context for tests.
//
// Implementing the interface by hand — rather than pulling in a mock package — is
// deliberate: a stub that kept compiling after ADK changed a callback contract
// would hide exactly the breakage these tests exist to catch.
type fakeCtx struct {
	context.Context
	state map[string]any
}

func newFakeCtx() *fakeCtx {
	return &fakeCtx{Context: context.Background(), state: map[string]any{}}
}

func (c *fakeCtx) UserContent() *genai.Content          { return nil }
func (c *fakeCtx) InvocationID() string                 { return "inv-1" }
func (c *fakeCtx) AgentName() string                    { return "test_agent" }
func (c *fakeCtx) ReadonlyState() session.ReadonlyState { return fakeState{c.state} }
func (c *fakeCtx) UserID() string                       { return "user" }
func (c *fakeCtx) AppName() string                      { return "app" }
func (c *fakeCtx) SessionID() string                    { return "sess" }
func (c *fakeCtx) Branch() string                       { return "" }
func (c *fakeCtx) IsolationScope() string               { return "" }
func (c *fakeCtx) Agent() agent.Agent                   { return nil }
func (c *fakeCtx) Artifacts() agent.Artifacts           { return nil }
func (c *fakeCtx) Memory() agent.Memory                 { return nil }
func (c *fakeCtx) Session() session.Session             { return nil }
func (c *fakeCtx) RunConfig() *agent.RunConfig          { return nil }
func (c *fakeCtx) EndInvocation()                       {}
func (c *fakeCtx) Ended() bool                          { return false }
func (c *fakeCtx) State() session.State                 { return fakeState{c.state} }
func (c *fakeCtx) FunctionCallID() string               { return "call-1" }
func (c *fakeCtx) Actions() *session.EventActions       { return &session.EventActions{} }
func (c *fakeCtx) ResumedInput(string) (any, bool)      { return nil, false }
func (c *fakeCtx) WithContext(ctx context.Context) agent.InvocationContext {
	c.Context = ctx
	return c
}
func (c *fakeCtx) WithICDelta(*agent.InvocationContextDelta) agent.InvocationContext { return c }
func (c *fakeCtx) SearchMemory(context.Context, string) (*memory.SearchResponse, error) {
	return &memory.SearchResponse{}, nil
}
func (c *fakeCtx) ToolConfirmation() *toolconfirmation.ToolConfirmation { return nil }
func (c *fakeCtx) RequestConfirmation(string, any) error                { return nil }

type fakeState struct{ m map[string]any }

func (s fakeState) Get(k string) (any, error) {
	v, ok := s.m[k]
	if !ok {
		return nil, session.ErrStateKeyNotExist
	}
	return v, nil
}
func (s fakeState) Set(k string, v any) error { s.m[k] = v; return nil }
func (s fakeState) All() iter.Seq2[string, any] {
	return func(yield func(string, any) bool) {
		for k, v := range s.m {
			if !yield(k, v) {
				return
			}
		}
	}
}

func (c *fakeCtx) Path() string                            { return "" }
func (c *fakeCtx) RunID() string                           { return "run-1" }
func (c *fakeCtx) SubScheduler() agent.DynamicSubScheduler { return nil }
func (c *fakeCtx) OutputForAncestors() []string            { return nil }
func (c *fakeCtx) WithAgentContext(ctx context.Context) agent.Context {
	c.Context = ctx
	return c
}
func (c *fakeCtx) WithAgentTimeout(timeout time.Duration) (agent.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(c.Context, timeout)
	c.Context = ctx
	return c, cancel
}
func (c *fakeCtx) WithAgentCancel() (agent.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(c.Context)
	c.Context = ctx
	return c, cancel
}
func (c *fakeCtx) WithDelta(*agent.CommonContextDelta) agent.Context { return c }

var _ agent.Context = (*fakeCtx)(nil)
