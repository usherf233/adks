package kit

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"

	"github.com/usherf233/adks/manifest"
)

// ProbeModel performs the cheapest possible real call: 16 output tokens.
//
// A probe that only constructs the client proves the key parses, not that it
// works. One tiny call is the difference between "configured" and "reachable".
func ProbeModel(ctx context.Context, spec manifest.ModelSpec) (string, error) {
	m, err := newModel(ctx, spec)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	tmp := int32(16)
	req := &model.LLMRequest{
		Model:    spec.Name,
		Contents: []*genai.Content{genai.NewContentFromText("Reply with the single word: ready", genai.RoleUser)},
		Config:   &genai.GenerateContentConfig{MaxOutputTokens: tmp},
	}
	for resp, err := range m.GenerateContent(ctx, req, false) {
		if err != nil {
			return "", err
		}
		if resp != nil && resp.Content != nil {
			for _, part := range resp.Content.Parts {
				if part != nil && part.Text != "" {
					return part.Text, nil
				}
			}
		}
	}
	return "responded with no text", nil
}

// ProbeSession creates and deletes one session, which is the only reliable way to
// know a DSN, a dialect and the schema migration all line up.
func ProbeSession(ctx context.Context, spec manifest.SessionSpec) (string, error) {
	// The budget has to cover connecting and migrating too. NewSessionService
	// ignores the context while gorm opens the driver, so run it under a select
	// and release a late connection as soon as it appears.
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	type openResult struct {
		svc session.Service
		err error
	}
	opened := make(chan openResult, 1)
	go func() {
		svc, err := NewSessionService(ctx, spec)
		opened <- openResult{svc: svc, err: err}
	}()

	var svc session.Service
	select {
	case r := <-opened:
		if r.err != nil {
			return "", r.err
		}
		svc = r.svc
	case <-ctx.Done():
		go func() {
			if r := <-opened; r.svc != nil {
				_ = CloseService(r.svc)
			}
		}()
		return "", fmt.Errorf("session service connect: %w", ctx.Err())
	}
	defer CloseService(svc)

	resp, err := svc.Create(ctx, &session.CreateRequest{
		AppName: "adks_probe",
		UserID:  "probe",
		State:   map[string]any{"temp:probe": true},
	})
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	id := resp.Session.ID()
	if err := svc.Delete(ctx, &session.DeleteRequest{AppName: "adks_probe", UserID: "probe", SessionID: id}); err != nil {
		return "", fmt.Errorf("delete probe session: %w", err)
	}
	return fmt.Sprintf("created and deleted session %s", id), nil
}
