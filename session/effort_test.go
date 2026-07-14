package session

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPinnedEffort pins the claude-only gate: the argv parsing itself is covered
// by agent.TestEffortFlag, so this exercises the one extra rule the Instance
// method adds — non-claude programs never report an effort, even when their
// program string carries the flag verbatim.
func TestPinnedEffort(t *testing.T) {
	for _, tc := range []struct {
		name, program, want string
	}{
		{"claude max", "claude --effort max", "max"},
		{"claude combined form", "claude --effort=low", "low"},
		{"claude path + flag", "/home/zvi/.local/bin/claude --effort xhigh", "xhigh"},
		{"claude no flag", "claude", ""},
		{"claude trailing bare flag", "claude --effort", ""},
		{"lookalike flag is not a pin", "claude --effort-budget high", ""},
		{"non-claude gated to empty", "aider --effort max", ""},
		{"codex gated to empty", "codex --effort max", ""},
		{"empty program", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inst, err := NewInstance(InstanceOptions{Title: "p", Path: ".", Program: tc.program})
			require.NoError(t, err)
			assert.Equal(t, tc.want, inst.PinnedEffort(), "program=%q", tc.program)
		})
	}
}

// TestEffortInfo pins the arbitration — the documented intent-then-truth split. The flag is
// intent and only fills the gap before the first resolved turn; the hook-reported level is
// claude's own post-downgrade truth and wins once known, including over an in-session
// /effort switch away from the flag (the whole point of the feature) and for a session that
// never passed --effort at all (where the flag can't see the settings.json/model default).
func TestEffortInfo(t *testing.T) {
	for _, tc := range []struct {
		name, program, runtime, want string
	}{
		{"no runtime falls back to the flag", "claude --effort max", "", "max"},
		{"no runtime, no flag", "claude", "", ""},
		{"runtime overrides the flag", "claude --effort max", "low", "low"},
		{"runtime with no flag reveals the inherited default", "claude", "high", "high"},
		{"runtime downgrade wins over the flag", "claude --effort max", "high", "high"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inst, err := NewInstance(InstanceOptions{Title: "p", Path: ".", Program: tc.program})
			require.NoError(t, err)
			inst.SetEffortMeta(tc.runtime)
			assert.Equal(t, tc.want, inst.EffortInfo())
		})
	}
}

// ComputeEffort reports nothing to apply for an unstarted session (no tmux), so the poll
// tick is a no-op until the session is live.
func TestComputeEffort_UnstartedIsNoop(t *testing.T) {
	inst, err := NewInstance(InstanceOptions{Title: "p", Path: ".", Program: "claude"})
	require.NoError(t, err)
	effort, ok := inst.ComputeEffort()
	assert.False(t, ok)
	assert.Empty(t, effort)
}
