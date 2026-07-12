package agent

import "testing"

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
