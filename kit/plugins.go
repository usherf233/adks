package kit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/plugin"
	"google.golang.org/adk/v2/plugin/loggingplugin"
	"google.golang.org/adk/v2/plugin/retryandreflect"
	"google.golang.org/adk/v2/tool"

	"github.com/usherf233/adks/manifest"
)

// newPlugins builds the plugin list referenced by the manifest. Unknown kinds are
// rejected here as well as in validation, because a plugin silently skipped is a
// promise the operator thinks they got.
func newPlugins(p *manifest.Project) ([]*plugin.Plugin, error) {
	out := make([]*plugin.Plugin, 0, len(p.Spec.Plugins))
	for _, ref := range p.Spec.Plugins {
		switch ref.Kind {
		case "builtin.logging":
			pl, err := loggingplugin.New(ref.Name)
			if err != nil {
				return nil, fmt.Errorf("plugin %q: %w", ref.Name, err)
			}
			out = append(out, pl)

		case "builtin.retryreflect":
			pl, err := retryandreflect.New()
			if err != nil {
				return nil, fmt.Errorf("plugin %q: %w", ref.Name, err)
			}
			out = append(out, pl)

		case "audit.webhook":
			pl, err := newWebhookPlugin(ref)
			if err != nil {
				return nil, err
			}
			out = append(out, pl)

		default:
			return nil, fmt.Errorf("plugin %q: unsupported kind %q", ref.Name, ref.Kind)
		}
	}
	return out, nil
}

// newWebhookPlugin posts one JSON object per tool call to an HTTP endpoint.
//
// A fire-and-forget POST is deliberate: an audit endpoint that is down must not
// stop the agent, and blocking a tool result on a network call would make the
// audit path a new failure mode.
func newWebhookPlugin(ref manifest.PluginRef) (*plugin.Plugin, error) {
	url := ref.Config["url"]
	if url == "" {
		return nil, fmt.Errorf("plugin %q: audit.webhook needs config.url", ref.Name)
	}
	if err := manifest.ValidateEndpoint(url); err != nil {
		return nil, fmt.Errorf("plugin %q: %w", ref.Name, err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	// Fire-and-forget must still be bounded: a slow endpoint plus a talkative
	// agent otherwise accumulates one goroutine per tool call forever.
	inflight := make(chan struct{}, 8)

	return plugin.New(plugin.Config{
		Name: ref.Name,
		AfterToolCallback: func(ctx agent.Context, t tool.Tool, args, result map[string]any, err error) (map[string]any, error) {
			ev := AuditEvent{
				TS:        time.Now().UTC(),
				SessionID: ctx.SessionID(),
				Tool:      t.Name(),
				ArgsHash:  Fingerprint(t.Name(), args),
				OK:        err == nil,
			}
			ev.Status = asInt(result["status"])
			if err != nil {
				ev.Error = err.Error()
			}
			body, mErr := json.Marshal(ev)
			if mErr != nil {
				return nil, nil
			}
			go func() {
				defer func() { _ = recover() }()
				select {
				case inflight <- struct{}{}:
					defer func() { <-inflight }()
				default:
					log.Printf("kit: audit webhook %q: too many in-flight posts, event dropped", ref.Name)
					return
				}
				resp, postErr := client.Post(url, "application/json", bytes.NewReader(body))
				if postErr != nil {
					log.Printf("kit: audit webhook %q: %v", ref.Name, postErr)
					return
				}
				resp.Body.Close()
			}()
			return nil, nil
		},
	})
}
