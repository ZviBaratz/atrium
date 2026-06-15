package session

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPinnedPermissionMode pins the claude-only gate: the argv parsing itself
// is covered by agent.TestPermissionModeFlag, so this exercises the one extra
// rule the Instance method adds — non-claude programs never report a mode, even
// when their program string carries the flag verbatim.
func TestPinnedPermissionMode(t *testing.T) {
	for _, tc := range []struct {
		name, program, want string
	}{
		{"claude plan", "claude --permission-mode plan", "plan"},
		{"claude combined form", "claude --permission-mode=acceptEdits", "acceptEdits"},
		{"claude path + flag", "/home/zvi/.local/bin/claude --permission-mode auto", "auto"},
		{"claude no flag", "claude", ""},
		{"claude trailing bare flag", "claude --permission-mode", ""},
		{"non-claude gated to empty", "aider --permission-mode plan", ""},
		{"codex gated to empty", "codex --permission-mode plan", ""},
		{"empty program", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inst, err := NewInstance(InstanceOptions{Title: "p", Path: ".", Program: tc.program})
			require.NoError(t, err)
			assert.Equal(t, tc.want, inst.PinnedPermissionMode(), "program=%q", tc.program)
		})
	}
}
