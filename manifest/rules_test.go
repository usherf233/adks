package manifest

import (
	"strings"
	"testing"
)

func TestValidSlugIsTheSharedRule(t *testing.T) {
	for _, s := range []string{"ab", "demo", "support-bot-2", "a1234567890", "trail-"} {
		if !ValidSlug(s) {
			t.Errorf("ValidSlug(%q) = false, want true", s)
		}
	}
	for _, s := range []string{
		"", "a", "UPPER", "../escape", `..\escape`, "a b", "a_b", "-lead",
		strings.Repeat("a", 42),
	} {
		if ValidSlug(s) {
			t.Errorf("ValidSlug(%q) = true, want false", s)
		}
	}
}

func TestHasErrorsIgnoresWarnings(t *testing.T) {
	if HasErrors(nil) {
		t.Fatal("empty issue list reported an error")
	}
	if HasErrors([]Issue{{Level: LevelWarn, Path: "a"}}) {
		t.Fatal("a warning was treated as an error")
	}
	if !HasErrors([]Issue{{Level: LevelWarn}, {Level: LevelError}}) {
		t.Fatal("an error among warnings was missed")
	}
}

func TestHostInAllowList(t *testing.T) {
	cases := []struct {
		host    string
		allowed []string
		want    bool
	}{
		{"api.example.com", []string{"API.example.com"}, true},
		{"api.example.com:8443", []string{"api.example.com"}, true},
		{"api.example.com:8443", []string{"api.example.com:8443"}, true},
		{"[::1]:8080", []string{"::1"}, true},
		{"[::1]:8080", []string{"[::1]"}, true},
		{"[::1]:8080", []string{"[::1]:8080"}, true},
		{"evil.example", []string{"api.example.com"}, false},
		{"api.example.com", nil, false},
	}
	for _, tc := range cases {
		if got := HostInAllowList(tc.host, tc.allowed); got != tc.want {
			t.Errorf("HostInAllowList(%q, %v) = %v, want %v", tc.host, tc.allowed, got, tc.want)
		}
	}
}

func TestToolNameRuleIsAgentName(t *testing.T) {
	if reToolName != reAgentName {
		t.Fatal("tool and agent names drifted into two different regexes")
	}
}
