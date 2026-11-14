package kit

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"

	"github.com/usherf233/adks/manifest"
)

// Errors that end a run for a reason worth naming in the transcript.
var (
	// ErrLLMLimit means the per-run model call budget ran out.
	ErrLLMLimit = errors.New("llm call limit reached")
	// ErrLoopDetected means the model repeated the same tool call too often.
	ErrLoopDetected = errors.New("repeated identical tool call")
)

// tempPrefix marks state that must not outlive the invocation, which is exactly
// the scope of a per-run counter.
const (
	stateKeyLLMCalls  = "temp:adks.llm_calls"
	stateKeyLoopLast  = "temp:adks.loop.last"
	stateKeyLoopCount = "temp:adks.loop.count"
)

// NewLimitCallbacks returns the before-model budget check.
//
// It returns an error rather than a canned LLMResponse. Both stop the loop, but
// an error carries the reason into the caller, where the UI turns it into
// "budget exhausted" instead of a message that looks like the model said it.
func NewLimitCallbacks(lim manifest.LimitsSpec) []llmagent.BeforeModelCallback {
	maxCalls := lim.MaxLLMCalls
	if maxCalls <= 0 {
		maxCalls = 20
	}
	return []llmagent.BeforeModelCallback{
		func(ctx agent.Context, _ *model.LLMRequest) (*model.LLMResponse, error) {
			if dl, ok := ctx.Deadline(); ok && !time.Now().Before(dl) {
				return nil, fmt.Errorf("%w: wall clock budget exhausted", ErrLLMLimit)
			}
			used := 0
			if v, err := ctx.State().Get(stateKeyLLMCalls); err == nil {
				if n, ok := v.(int); ok {
					used = n
				}
			}
			if used >= maxCalls {
				return nil, fmt.Errorf("%w: %d calls in one turn", ErrLLMLimit, maxCalls)
			}
			return nil, ctx.State().Set(stateKeyLLMCalls, used+1)
		},
	}
}

// NewLoopGuard stops a run whose model keeps issuing the same call.
//
// A model that loses the tool result will happily retry the identical call until
// the budget runs out. Three identical calls in a row is enough evidence; a
// different call in between resets the count, because that means progress.
func NewLoopGuard() llmagent.BeforeToolCallback {
	return func(ctx agent.Context, t tool.Tool, args map[string]any) (map[string]any, error) {
		fp := Fingerprint(t.Name(), args)
		// temp: state is scoped to one invocation. A process-level counter lives as
		// long as the cached agent does, so three different sessions each calling a
		// tool once would be misread as one session looping.
		last := ""
		if v, err := ctx.State().Get(stateKeyLoopLast); err == nil {
			if s, ok := v.(string); ok {
				last = s
			}
		}
		n := 0
		if v, err := ctx.State().Get(stateKeyLoopCount); err == nil {
			if i, ok := v.(int); ok {
				n = i
			}
		}
		if fp == last {
			n++
		} else {
			last, n = fp, 1
		}
		if err := ctx.State().Set(stateKeyLoopLast, last); err != nil {
			return nil, err
		}
		if err := ctx.State().Set(stateKeyLoopCount, n); err != nil {
			return nil, err
		}
		if n >= 3 {
			return nil, fmt.Errorf("%w: %s called %d times with the same arguments; change the arguments or answer without it",
				ErrLoopDetected, t.Name(), n)
		}
		return nil, nil
	}
}

// AuditSink appends one JSON object per line.
type AuditSink struct {
	Path string
	// Verbose records raw arguments. Off by default: arguments routinely carry
	// personal data, and an audit log is the last place it should accumulate.
	Verbose bool

	mu sync.Mutex
}

// AuditEvent is one line of the audit log.
type AuditEvent struct {
	TS        time.Time `json:"ts"`
	SessionID string    `json:"sessionId"`
	Tool      string    `json:"tool"`
	ArgsHash  string    `json:"argsHash"`
	Status    int       `json:"status,omitempty"`
	OK        bool      `json:"ok"`
	DurMs     int64     `json:"durMs,omitempty"`
	Truncated bool      `json:"truncated,omitempty"`
	Error     string    `json:"error,omitempty"`
	Args      any       `json:"args,omitempty"`
}

// NewAuditSink creates the parent directory so the first write cannot fail.
func NewAuditSink(path string, verbose bool) (*AuditSink, error) {
	if path == "" {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("audit sink: %w", err)
	}
	return &AuditSink{Path: path, Verbose: verbose}, nil
}

// Write appends one event. A failure to audit must not fail the tool call, so the
// caller logs the error and continues.
func (s *AuditSink) Write(ev AuditEvent) error {
	if s == nil || s.Path == "" {
		return nil
	}
	buf, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(buf, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

// NewAuditCallbacks records one line per tool call.
func NewAuditCallbacks(sink *AuditSink) []llmagent.AfterToolCallback {
	if sink == nil {
		return nil
	}
	return []llmagent.AfterToolCallback{
		func(ctx agent.Context, t tool.Tool, args, result map[string]any, err error) (map[string]any, error) {
			ev := AuditEvent{
				TS:        time.Now().UTC(),
				SessionID: ctx.SessionID(),
				Tool:      t.Name(),
				ArgsHash:  Fingerprint(t.Name(), args),
			}
			if sink.Verbose {
				ev.Args = args
			}
			ev.Status = asInt(result["status"])
			ev.DurMs = int64(asInt(result["durMs"]))
			if tr, ok := result["truncated"].(bool); ok {
				ev.Truncated = tr
			}
			if ok, present := result["ok"].(bool); present {
				ev.OK = ok && err == nil
			} else {
				ev.OK = err == nil
			}
			if err != nil {
				ev.Error = err.Error()
			}
			_ = sink.Write(ev)
			// Returning nil, nil keeps the tool result untouched.
			return nil, nil
		},
	}
}
