package kit

import (
	"context"
	"fmt"
	"iter"
	"os"
	"strings"

	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/model/anthropicmodel"
	"google.golang.org/adk/v2/model/gemini"
	"google.golang.org/adk/v2/model/openaimodel"
	"google.golang.org/genai"

	"github.com/usherf233/adks/manifest"
)

// newModel builds the LLM client. Credentials are read here and only here, so a
// missing key fails at build time instead of on the first token.
func newModel(ctx context.Context, s manifest.ModelSpec) (model.LLM, error) {
	key := ""
	if s.APIKeyEnv != "" {
		key = os.Getenv(s.APIKeyEnv)
	}
	missingKey := fmt.Errorf("model %s/%s: environment variable %s is empty; export it before running",
		s.Provider, s.Name, s.APIKeyEnv)

	switch s.Provider {
	case manifest.ProviderGemini, "":
		if key == "" {
			return nil, missingKey
		}
		m, err := gemini.NewModel(ctx, s.Name, &genai.ClientConfig{APIKey: key})
		if err != nil {
			return nil, fmt.Errorf("create gemini model %q: %w", s.Name, err)
		}
		return m, nil

	case manifest.ProviderOpenAI:
		if key == "" {
			return nil, missingKey
		}
		m, err := openaimodel.NewModel(ctx, s.Name, &openaimodel.ClientConfig{
			APIKey:  key,
			BaseURL: strings.TrimSpace(s.BaseURL),
		})
		if err != nil {
			return nil, fmt.Errorf("create openai model %q: %w", s.Name, err)
		}
		return m, nil

	case manifest.ProviderAnthropic:
		if key == "" {
			return nil, missingKey
		}
		m, err := anthropicmodel.NewModel(ctx, s.Name, anthropicmodel.ClientConfig{
			APIKey:  key,
			BaseURL: strings.TrimSpace(s.BaseURL),
		})
		if err != nil {
			return nil, fmt.Errorf("create anthropic model %q: %w", s.Name, err)
		}
		return m, nil
	}
	return nil, fmt.Errorf("unsupported model provider %q", s.Provider)
}

// generationConfig maps the manifest sampling knobs onto genai.
func generationConfig(g *manifest.GenSpec) *genai.GenerateContentConfig {
	if g == nil {
		return nil
	}
	cfg := &genai.GenerateContentConfig{}
	set := false
	if g.Temperature != nil {
		cfg.Temperature = g.Temperature
		set = true
	}
	if g.TopP != nil {
		cfg.TopP = g.TopP
		set = true
	}
	if g.MaxOutputTokens != nil {
		cfg.MaxOutputTokens = *g.MaxOutputTokens
		set = true
	}
	if !set {
		return nil
	}
	return cfg
}

// unavailableModel stands in for a model when a build deliberately skips the
// credential check. Calling it is an error: the point of the flag is to compile
// and inspect a project, not to talk to a provider.
type unavailableModel struct{ reason string }

func (m *unavailableModel) Name() string { return "unavailable" }

func (m *unavailableModel) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(nil, fmt.Errorf("model unavailable: %s", m.reason))
	}
}
