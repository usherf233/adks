package manifest

import (
	"crypto/rand"
	"encoding/base32"
	"strings"
	"time"
)

// NewID returns a "prj_" prefixed identifier, 12 base32 characters wide.
func NewID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is not recoverable in a meaningful way; fall back
		// to a time based id rather than panicking inside the web server.
		return "prj_" + base32.StdEncoding.EncodeToString([]byte(time.Now().Format(time.RFC3339Nano)))[:12]
	}
	s := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])
	return "prj_" + strings.ToLower(s)[:12]
}

// New builds a runnable starter project: one Gemini agent, no tools, sqlite
// sessions. The instruction is deliberately concrete enough to answer a first
// message without further editing.
func New(slug, name string) *Project {
	now := time.Now()
	p := &Project{
		APIVersion: APIVersion,
		Kind:       Kind,
		Metadata: Metadata{
			ID:      NewID(),
			Slug:    slug,
			Name:    name,
			Created: now,
			Updated: now,
		},
	}
	p.Spec.Agent = AgentSpec{
		Name:        agentNameFromSlug(slug),
		Description: "A general purpose assistant.",
		Instruction: "You are a helpful assistant. Answer in the language the user writes in.\n" +
			"When you are unsure, say what you do not know instead of guessing.",
		Model: ModelSpec{Provider: ProviderGemini},
	}
	ApplyDefaults(p)
	return p
}

// agentNameFromSlug turns "support-bot" into "support_bot_agent" so the default
// passes validation for any legal slug.
func agentNameFromSlug(slug string) string {
	s := strings.ReplaceAll(slug, "-", "_")
	if len(s) > 48 {
		s = s[:48]
	}
	return s + "_agent"
}
