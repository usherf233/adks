package kit

import (
	"errors"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	"github.com/usherf233/adks/manifest"
)

func usageResp(prompt, candidates, thoughts int32) *model.LLMResponse {
	return &model.LLMResponse{UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount:     prompt,
		CandidatesTokenCount: candidates,
		ThoughtsTokenCount:   thoughts,
	}}
}

// TestTokenBudgetDisabledWhenZero keeps "unset" honest: no callback at all, so an
// unlimited project pays nothing for the feature.
func TestTokenBudgetDisabledWhenZero(t *testing.T) {
	if cbs := NewTokenBudgetCallbacks(manifest.LimitsSpec{}); cbs != nil {
		t.Fatalf("an unset budget installed %d callbacks", len(cbs))
	}
	if cbs := NewTokenBudgetCallbacks(manifest.LimitsSpec{MaxTokensPerTurn: -1}); cbs != nil {
		t.Fatalf("a negative budget installed %d callbacks", len(cbs))
	}
}

// TestTokenBudgetAccumulatesAcrossCalls is the reason the counter lives in state:
// the budget is per turn, and a turn is several model calls.
func TestTokenBudgetAccumulatesAcrossCalls(t *testing.T) {
	ctx := newFakeCtx()
	cbs := NewTokenBudgetCallbacks(manifest.LimitsSpec{MaxTokensPerTurn: 100})
	if len(cbs) != 1 {
		t.Fatalf("got %d callbacks, want 1", len(cbs))
	}

	// 60 tokens, then 30: still under.
	for _, r := range []*model.LLMResponse{usageResp(50, 10, 0), usageResp(20, 10, 0)} {
		if _, err := cbs[0](ctx, r, nil); err != nil {
			t.Fatalf("stopped too early: %v", err)
		}
	}
	// 30 more pushes it to 120.
	_, err := cbs[0](ctx, usageResp(20, 10, 0), nil)
	if !errors.Is(err, ErrTokenBudget) {
		t.Fatalf("err = %v, want ErrTokenBudget", err)
	}
}

// TestTokenBudgetCountsThoughtTokens: reasoning tokens are billed, so they must
// count against the budget or a thinking model slips past it.
func TestTokenBudgetCountsThoughtTokens(t *testing.T) {
	ctx := newFakeCtx()
	cbs := NewTokenBudgetCallbacks(manifest.LimitsSpec{MaxTokensPerTurn: 100})
	_, err := cbs[0](ctx, usageResp(10, 10, 200), nil)
	if !errors.Is(err, ErrTokenBudget) {
		t.Fatalf("thought tokens were not counted: %v", err)
	}
}

// TestTokenBudgetIgnoresResponsesWithoutUsage: a provider that reports nothing
// must not be treated as having spent nothing but "safe" — the check simply
// cannot judge, so it stays out of the way.
func TestTokenBudgetIgnoresResponsesWithoutUsage(t *testing.T) {
	ctx := newFakeCtx()
	cbs := NewTokenBudgetCallbacks(manifest.LimitsSpec{MaxTokensPerTurn: 1})
	cases := []struct {
		name string
		resp *model.LLMResponse
		err  error
	}{
		{"nil response", nil, nil},
		{"no usage", &model.LLMResponse{}, nil},
		{"model error", usageResp(999, 999, 999), errors.New("boom")},
	}
	for _, tc := range cases {
		if _, err := cbs[0](ctx, tc.resp, tc.err); err != nil {
			t.Errorf("%s: unexpected stop: %v", tc.name, err)
		}
	}
}
