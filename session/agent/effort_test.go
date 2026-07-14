package agent

import (
	"strings"
	"testing"
)

func TestValidEffort(t *testing.T) {
	for _, level := range []string{"low", "medium", "high", "xhigh", "max"} {
		if !ValidEffort(level) {
			t.Errorf("ValidEffort(%q) = false, want true", level)
		}
	}
	for _, bad := range []string{"", "ultracode", "LOW", "extreme", "hi"} {
		if ValidEffort(bad) {
			t.Errorf("ValidEffort(%q) = true, want false", bad)
		}
	}
}

func TestClaudeEffortLabels_ParityWithLevels(t *testing.T) {
	if len(ClaudeEffortLabels) != len(ClaudeEffortLevels) {
		t.Fatalf("labels len %d != levels len %d", len(ClaudeEffortLabels), len(ClaudeEffortLevels))
	}
	if want := []string{"low", "med", "high", "xhigh", "max"}; strings.Join(ClaudeEffortLabels, ",") != strings.Join(want, ",") {
		t.Errorf("ClaudeEffortLabels = %v, want %v (the form's width budget depends on med)", ClaudeEffortLabels, want)
	}
}

// TestClaudeEffortLabel covers the shared level→label lookup both chip surfaces use. An
// unmapped level renders verbatim rather than vanishing: the hook reports claude's own
// resolved level, so a level a newer CLI adds should still show up as itself.
func TestClaudeEffortLabel(t *testing.T) {
	for level, want := range map[string]string{
		"low":    "low",
		"medium": "med",
		"high":   "high",
		"xhigh":  "xhigh",
		"max":    "max",
		"ultra":  "ultra", // hypothetical future level: verbatim, not dropped
		"":       "",
	} {
		if got := ClaudeEffortLabel(level); got != want {
			t.Errorf("ClaudeEffortLabel(%q) = %q, want %q", level, got, want)
		}
	}
}

func TestEffortFlag(t *testing.T) {
	tests := []struct{ name, program, want string }{
		{"no pin", "claude", ""},
		{"empty program", "", ""},
		{"separate form", "claude --effort max", "max"},
		{"combined form", "claude --effort=max", "max"},
		{"among other flags", "claude --model opus --effort low --permission-mode plan", "low"},
		{"last pin wins", "claude --effort low --effort high", "high"},
		{"bare trailing flag has no value", "claude --effort", ""},
		// Whole-field compare: a lookalike flag must not be read as an --effort
		// pin (the hasFlag trap the plan names).
		{"lookalike flag ignored", "claude --effort-budget high", ""},
		{"lookalike combined ignored", "claude --effort-budget=high", ""},
		{"lookalike does not shadow real pin", "claude --effort-budget high --effort max", "max"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EffortFlag(tt.program); got != tt.want {
				t.Errorf("EffortFlag(%q) = %q, want %q", tt.program, got, tt.want)
			}
		})
	}
}

func TestWithEffortFlag(t *testing.T) {
	tests := []struct{ name, program, level, want string }{
		{"append to bare", "claude", "xhigh", "claude --effort xhigh"},
		{"append after other flags", "claude --model opus", "high", "claude --model opus --effort high"},
		{"replace existing pin", "claude --effort low", "max", "claude --effort max"},
		{"replace combined form", "claude --effort=low", "max", "claude --effort max"},
		{"quoted program appends, last wins", `claude --effort low --settings '/a b'`, "high", `claude --effort low --settings '/a b' --effort high`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := WithEffortFlag(tt.program, tt.level); got != tt.want {
				t.Errorf("WithEffortFlag(%q, %q) = %q, want %q", tt.program, tt.level, got, tt.want)
			}
		})
	}
}
