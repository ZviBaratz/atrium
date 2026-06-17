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

// TestPermissionModeInfo pins the arbitration: the detected live mode wins over
// the launch flag (the staleness fix), and falls back to the flag only before
// any detection. A detected "default" is returned verbatim — the renderer hides
// the chip for it, so a switch back to normal clears the chip.
func TestPermissionModeInfo(t *testing.T) {
	for _, tc := range []struct {
		name, program, runtime, want string
	}{
		{"no detection falls back to flag", "claude --permission-mode plan", "", "plan"},
		{"no detection, no flag", "claude", "", ""},
		{"live mode overrides flag", "claude --permission-mode plan", "auto", "auto"},
		{"live mode with no flag", "claude", "acceptEdits", "acceptEdits"},
		{"live default clears a pinned mode", "claude --permission-mode plan", "default", "default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inst, err := NewInstance(InstanceOptions{Title: "p", Path: ".", Program: tc.program})
			require.NoError(t, err)
			inst.SetModeMeta(tc.runtime)
			assert.Equal(t, tc.want, inst.PermissionModeInfo())
		})
	}
}

// ComputeMode reports nothing to apply for an unstarted session (no tmux), so
// the poll tick is a no-op until the session is live.
func TestComputeMode_UnstartedIsNoop(t *testing.T) {
	inst, err := NewInstance(InstanceOptions{Title: "p", Path: ".", Program: "claude"})
	require.NoError(t, err)
	mode, ok := inst.ComputeMode()
	assert.False(t, ok)
	assert.Empty(t, mode)
}
