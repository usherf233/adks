package kit

import (
	"encoding/json"
	"fmt"
	"iter"
	"regexp"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"
	"google.golang.org/genai"

	"github.com/usherf233/adks/manifest"
)

// routeNode is the deterministic branch node.
//
// ADK routes on event.Routes, and AgentNode never sets that field — so a graph
// cannot branch on an LLM's answer directly. This node fills that gap without
// user-supplied Go code: it reads the upstream output as text, applies the
// configured rules in order, and emits one event carrying the matching label.
// A graph built purely from a manifest therefore can branch, but only on rules
// that fit these operators.
type routeNode struct {
	workflow.BaseNode
	rules    []compiledRule
	inputKey string
}

type compiledRule struct {
	op    string
	match string
	route string
	re    *regexp.Regexp
}

func newRouteNode(spec manifest.NodeSpec, cfg workflow.NodeConfig) (*routeNode, error) {
	n := &routeNode{
		BaseNode: workflow.NewBaseNode(spec.ID, spec.Description, cfg),
		inputKey: spec.Input,
	}
	for _, r := range spec.Rules {
		cr := compiledRule{op: r.Op, match: r.Match, route: r.Route}
		if cr.op == "" {
			cr.op = "contains"
		}
		if cr.op == "regex" {
			re, err := regexp.Compile(cr.match)
			if err != nil {
				return nil, fmt.Errorf("rule %q: %w", cr.route, err)
			}
			cr.re = re
		}
		n.rules = append(n.rules, cr)
	}
	return n, nil
}

func (n *routeNode) Run(ctx agent.Context, input any) iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		text, err := n.text(ctx, input)
		if err != nil {
			yield(nil, err)
			return
		}
		ev := session.NewEvent(ctx, ctx.InvocationID())
		// The text is forwarded as output so a router does not swallow the user
		// message: downstream agent nodes need it as their own input.
		ev.Output = text
		if label, ok := n.match(text); ok {
			ev.Routes = []string{label}
		}
		yield(ev, nil)
	}
}

func (n *routeNode) match(text string) (string, bool) {
	for _, r := range n.rules {
		if ruleMatches(r, text) {
			return r.route, true
		}
	}
	return "", false
}

func (n *routeNode) text(ctx agent.Context, input any) (string, error) {
	if n.inputKey != "" {
		val, err := ctx.ReadonlyState().Get(n.inputKey)
		if err != nil {
			return "", fmt.Errorf("route node %q: read state key %q: %w", n.Name(), n.inputKey, err)
		}
		return textOf(val), nil
	}
	return textOf(input), nil
}

// ruleMatches evaluates one rule. contains/prefix/suffix fold case, because a
// router usually answers "what is this message about" rather than exact bytes.
func ruleMatches(r compiledRule, text string) bool {
	switch r.op {
	case "always":
		return true
	case "equals":
		return strings.TrimSpace(text) == r.match
	case "contains":
		return strings.Contains(strings.ToLower(text), strings.ToLower(r.match))
	case "prefix":
		return strings.HasPrefix(strings.ToLower(strings.TrimSpace(text)), strings.ToLower(r.match))
	case "suffix":
		return strings.HasSuffix(strings.ToLower(strings.TrimSpace(text)), strings.ToLower(r.match))
	case "regex":
		return r.re != nil && r.re.MatchString(text)
	}
	return false
}

// textOf renders a node input as matchable text. Agent outputs arrive as a
// string or a *genai.Content; anything structured is rendered as JSON so a rule
// can still match a field value.
func textOf(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []byte:
		return string(t)
	case *genai.Content:
		var sb strings.Builder
		for _, part := range t.Parts {
			if part != nil && part.Text != "" {
				sb.WriteString(part.Text)
			}
		}
		return sb.String()
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprint(v)
		}
		return string(b)
	}
}

var _ workflow.Node = (*routeNode)(nil)
