package kit

import (
	"strings"
	"testing"
)

// TestEstimateASCIITokensMatchesEstimateTokens keeps the allocation-free path
// honest: callers that only know a byte count must get the same answer as the
// caller holding the actual string, or the estimate silently splits in two.
func TestEstimateASCIITokensMatchesEstimateTokens(t *testing.T) {
	for _, n := range []int{0, 1, 3, 4, 40, 600, 6000} {
		want := EstimateTokens(strings.Repeat("x", n))
		if got := EstimateASCIITokens(n); got != want {
			t.Errorf("EstimateASCIITokens(%d) = %d, EstimateTokens gave %d", n, got, want)
		}
	}
}

func TestEstimateASCIITokensBounds(t *testing.T) {
	if got := EstimateASCIITokens(0); got != 0 {
		t.Errorf("EstimateASCIITokens(0) = %d, want 0", got)
	}
	if got := EstimateASCIITokens(-100); got != 0 {
		t.Errorf("EstimateASCIITokens(negative) = %d, want 0", got)
	}
}

// TestEstimateTokensSplitsWideCharacters is the reason there are two coefficients:
// a single chars-per-token ratio underestimates CJK by about a factor of two.
func TestEstimateTokensSplitsWideCharacters(t *testing.T) {
	ascii := EstimateTokens(strings.Repeat("a", 38))
	wide := EstimateTokens(strings.Repeat("字", 17))
	// 38 ASCII chars ≈ 10 tokens; 17 wide chars ≈ 10 tokens.
	if ascii != 10 || wide != 10 {
		t.Fatalf("coefficients drifted: ascii=%d wide=%d, want 10 and 10", ascii, wide)
	}
	if EstimateTokens(strings.Repeat("字", 38)) >= ascii*3 {
		t.Fatal("wide characters are being counted at the ASCII ratio")
	}
}
